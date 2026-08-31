package blob

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Store struct {
	Root           string
	MaxBundleBytes int64
	MaxFiles       int
}

var _ Backend = Store{}

type Manifest struct {
	Schema     string         `json:"schema"`
	Title      string         `json:"title"`
	Entrypoint string         `json:"entrypoint"`
	Files      []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type"`
}

type Staged struct {
	UploadID  string
	Directory string
	Manifest  Manifest
	Digest    string
	Bytes     int64
}

// ValidationError reports a bundle problem that is safe to return to the
// uploader. All other StageZIP errors are operational details and must remain
// server-side.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func validationErrorf(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

func IsValidationError(err error) bool {
	var validationErr *ValidationError
	return errors.As(err, &validationErr)
}

func (s Store) Init(_ context.Context) error {
	if s.Root == "" || s.MaxBundleBytes <= 0 || s.MaxFiles <= 0 {
		return errors.New("invalid blob store configuration")
	}
	for _, dir := range []string{s.Root, filepath.Join(s.Root, ".uploads")} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) StageZIP(ctx context.Context, uploadID, title, entrypoint string, r io.Reader) (Staged, error) {
	if err := s.Init(ctx); err != nil {
		return Staged{}, err
	}
	entrypoint, err := cleanRelative(entrypoint)
	if err != nil {
		return Staged{}, validationErrorf("invalid entrypoint: %v", err)
	}
	stageDir := filepath.Join(s.Root, ".uploads", uploadID)
	if err := os.Mkdir(stageDir, 0750); err != nil {
		return Staged{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stageDir)
		}
	}()

	zipPath := filepath.Join(stageDir, "bundle.zip")
	zf, err := os.OpenFile(zipPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		return Staged{}, err
	}
	written, copyErr := io.Copy(zf, io.LimitReader(r, s.MaxBundleBytes+1))
	closeErr := zf.Close()
	if copyErr != nil {
		return Staged{}, copyErr
	}
	if closeErr != nil {
		return Staged{}, closeErr
	}
	if written > s.MaxBundleBytes {
		return Staged{}, validationErrorf("bundle exceeds %d bytes", s.MaxBundleBytes)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return Staged{}, validationErrorf("invalid zip archive: %v", err)
	}
	defer zr.Close()
	if len(zr.File) > s.MaxFiles {
		return Staged{}, validationErrorf("bundle contains more than %d files", s.MaxFiles)
	}

	filesDir := filepath.Join(stageDir, "files")
	if err := os.Mkdir(filesDir, 0750); err != nil {
		return Staged{}, err
	}
	manifest := Manifest{Schema: "rendercase/v1", Title: strings.TrimSpace(title), Entrypoint: entrypoint}
	seen := make(map[string]struct{})
	var total int64
	entryFound := false
	for _, zf := range zr.File {
		if err := ctx.Err(); err != nil {
			return Staged{}, err
		}
		if strings.Contains(zf.Name, "..") {
			return Staged{}, validationErrorf("invalid zip path %q: path contains '..'", zf.Name)
		}
		name, err := cleanRelative(zf.Name)
		if err != nil {
			return Staged{}, validationErrorf("invalid zip path %q: %v", zf.Name, err)
		}
		if zf.FileInfo().IsDir() {
			continue
		}
		if zf.Mode()&os.ModeSymlink != 0 || !zf.Mode().IsRegular() {
			return Staged{}, validationErrorf("zip path %q is not a regular file", zf.Name)
		}
		if _, exists := seen[name]; exists {
			return Staged{}, validationErrorf("duplicate zip path %q", name)
		}
		seen[name] = struct{}{}
		if name == entrypoint {
			entryFound = true
		}
		remaining := s.MaxBundleBytes - total
		if zf.UncompressedSize64 > uint64(remaining) {
			return Staged{}, validationErrorf("expanded bundle exceeds %d bytes", s.MaxBundleBytes)
		}
		destination, err := containedPath(filesDir, name)
		if err != nil {
			return Staged{}, validationErrorf("invalid zip path %q: %v", zf.Name, err)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
			return Staged{}, err
		}
		source, err := zf.Open()
		if err != nil {
			return Staged{}, err
		}
		dest, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
		if err != nil {
			source.Close()
			return Staged{}, err
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(dest, h), io.LimitReader(source, remaining+1))
		sourceErr := source.Close()
		destErr := dest.Close()
		if copyErr != nil || sourceErr != nil || destErr != nil {
			return Staged{}, errors.Join(copyErr, sourceErr, destErr)
		}
		if n > remaining {
			return Staged{}, validationErrorf("expanded bundle exceeds %d bytes", s.MaxBundleBytes)
		}
		if uint64(n) != zf.UncompressedSize64 {
			return Staged{}, validationErrorf("zip path %q has inconsistent uncompressed size", zf.Name)
		}
		total += n
		contentType := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		manifest.Files = append(manifest.Files, ManifestFile{
			Path: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), ContentType: contentType,
		})
	}
	if !entryFound {
		return Staged{}, validationErrorf("entrypoint %q is not present", entrypoint)
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return Staged{}, err
	}
	digest := sha256.Sum256(manifestJSON)
	if err := os.WriteFile(filepath.Join(stageDir, "manifest.json"), append(manifestJSON, '\n'), 0640); err != nil {
		return Staged{}, err
	}
	if err := os.Remove(zipPath); err != nil {
		return Staged{}, err
	}
	cleanup = false
	return Staged{UploadID: uploadID, Directory: stageDir, Manifest: manifest, Digest: hex.EncodeToString(digest[:]), Bytes: total}, nil
}

func (s Store) Publish(staged Staged, artifactID string, version int) (string, error) {
	if version < 1 {
		return "", errors.New("version must be positive")
	}
	relative := filepath.Join(artifactID, fmt.Sprintf("v%06d", version))
	destination := filepath.Join(s.Root, relative)
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return "", err
	}
	if err := os.Rename(staged.Directory, destination); err != nil {
		return "", err
	}
	return filepath.ToSlash(relative), nil
}

// Staged returns the filesystem location and metadata for a staged upload.
func (s Store) Staged(uploadID string, manifest Manifest, digest string, bytes int64) Staged {
	return Staged{UploadID: uploadID, Directory: filepath.Join(s.Root, ".uploads", uploadID), Manifest: manifest, Digest: digest, Bytes: bytes}
}

// PublishUpload moves a staged upload to its immutable object directory. Database
// versions refer to this directory, so the object name need not predict the next
// version number and concurrent commits cannot collide.
func (s Store) PublishUpload(_ context.Context, staged Staged, artifactID string) (string, error) {
	relative := filepath.Join(artifactID, "objects", staged.UploadID)
	destination := filepath.Join(s.Root, relative)
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return "", err
	}
	if err := os.Rename(staged.Directory, destination); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if info, statErr := os.Stat(destination); statErr == nil && info.IsDir() {
				return filepath.ToSlash(relative), nil
			}
		}
		return "", err
	}
	return filepath.ToSlash(relative), nil
}

func (s Store) Open(_ context.Context, objectDir, name string) (Object, error) {
	objectDir, err := cleanRelative(filepath.ToSlash(objectDir))
	if err != nil {
		return Object{}, err
	}
	name, err = cleanRelative(name)
	if err != nil {
		return Object{}, err
	}
	root := filepath.Join(s.Root, filepath.FromSlash(objectDir), "files")
	file := filepath.Join(root, filepath.FromSlash(name))
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Object{}, errors.New("path escapes artifact")
	}
	f, err := os.Open(file)
	if err != nil {
		return Object{}, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = errors.New("artifact path is not a regular file")
		}
		return Object{}, err
	}
	return Object{Body: f, Size: info.Size(), LastModified: info.ModTime()}, nil
}

func (s Store) RemoveStage(uploadID string) error {
	return os.RemoveAll(filepath.Join(s.Root, ".uploads", uploadID))
}

func (s Store) CleanupStages(olderThan time.Time) (int, error) {
	directory := filepath.Join(s.Root, ".uploads")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return removed, err
		}
		if info.ModTime().Before(olderThan) {
			if err := os.RemoveAll(filepath.Join(directory, entry.Name())); err != nil {
				return removed, err
			}
			removed++
		}
	}
	return removed, nil
}

func cleanRelative(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if strings.ContainsRune(name, 0) || strings.HasPrefix(name, "/") {
		return "", errors.New("path must be relative")
	}
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path escapes bundle")
	}
	return cleaned, nil
}

func containedPath(root, relative string) (string, error) {
	destination := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, destination)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("path escapes bundle")
	}
	return destination, nil
}
