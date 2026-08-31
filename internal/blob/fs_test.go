package blob

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestStoreOpenRejectsObjectDirectoryTraversal(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBundleBytes: 1024, MaxFiles: 10}
	if _, err := store.Open(context.Background(), "../escape", "index.html"); err == nil {
		t.Fatal("object directory traversal accepted")
	}
}

func TestCleanupStagesRemovesOnlyOldDirectories(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root, MaxBundleBytes: 1024, MaxFiles: 10}
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, ".uploads", "old")
	fresh := filepath.Join(root, ".uploads", "fresh")
	for _, directory := range []string{old, fresh} {
		if err := os.Mkdir(directory, 0750); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	removed, err := store.CleanupStages(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed %d stage directories, want 1", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old stage still exists: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh stage removed: %v", err)
	}
}

func TestStagePublishOpen(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBundleBytes: 1024, MaxFiles: 10}
	data := zipBytes(t, map[string]string{"index.html": "<h1>Hello</h1>", "app.js": "ok"})
	staged, err := store.StageZIP(context.Background(), "upload1", "Hello", "index.html", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if staged.Manifest.Entrypoint != "index.html" || len(staged.Manifest.Files) != 2 {
		t.Fatalf("unexpected manifest: %+v", staged.Manifest)
	}
	objectDir, err := store.Publish(staged, "artifact1", 1)
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Open(context.Background(), objectDir, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	object.Body.Close()
	if _, err := os.Stat(filepath.Join(store.Root, objectDir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsTraversalAndMissingEntrypoint(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBundleBytes: 1024, MaxFiles: 10}
	data := zipBytes(t, map[string]string{"../escape": "bad", "other.html": "ok"})
	if _, err := store.StageZIP(context.Background(), "upload1", "Bad", "index.html", bytes.NewReader(data)); err == nil {
		t.Fatal("traversal bundle accepted")
	} else if !IsValidationError(err) {
		t.Fatalf("traversal error is not safe validation error: %T: %v", err, err)
	}
	data = zipBytes(t, map[string]string{"other.html": "ok"})
	if _, err := store.StageZIP(context.Background(), "upload2", "Bad", "index.html", bytes.NewReader(data)); err == nil {
		t.Fatal("missing entrypoint accepted")
	}
}

func TestContainedPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../escape", "dir/../../escape"} {
		if _, err := containedPath(root, name); err == nil {
			t.Errorf("containedPath accepted %q", name)
		}
	}
	want := filepath.Join(root, "assets", "bundle.js")
	if got, err := containedPath(root, "assets/bundle.js"); err != nil || got != want {
		t.Fatalf("contained path = %q, %v; want %q", got, err, want)
	}
}

func TestStageZIPRejectsAnyDoubleDotEntry(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBundleBytes: 1024, MaxFiles: 10}
	data := zipBytes(t, map[string]string{"index.html": "ok", "bundle..js": "no"})
	if _, err := store.StageZIP(context.Background(), "upload1", "Bad", "index.html", bytes.NewReader(data)); err == nil {
		t.Fatal("double-dot ZIP entry accepted")
	} else if !IsValidationError(err) {
		t.Fatalf("double-dot rejection is not a validation error: %T: %v", err, err)
	}
}

func TestStageZIPDoesNotClassifyFilesystemErrorsAsValidation(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBundleBytes: 1024, MaxFiles: 10}
	data := zipBytes(t, map[string]string{"index.html": "ok"})
	if _, err := store.StageZIP(context.Background(), "upload1", "First", "index.html", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StageZIP(context.Background(), "upload1", "Second", "index.html", bytes.NewReader(data)); err == nil {
		t.Fatal("duplicate staging directory accepted")
	} else if IsValidationError(err) {
		t.Fatalf("filesystem error exposed as validation error: %v", err)
	}
}

func TestRejectsSpoofedUncompressedSize(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBundleBytes: 1024, MaxFiles: 10}
	data := zipBytes(t, map[string]string{"index.html": "this content is longer than one byte"})
	central := bytes.Index(data, []byte{'P', 'K', 1, 2})
	if central < 0 {
		t.Fatal("central directory not found")
	}
	binary.LittleEndian.PutUint32(data[central+24:central+28], 1)
	if _, err := store.StageZIP(context.Background(), "upload1", "Bad", "index.html", bytes.NewReader(data)); err == nil {
		t.Fatal("bundle with spoofed uncompressed size accepted")
	}
}
