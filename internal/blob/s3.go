package blob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3Config struct {
	Bucket, Prefix, Region, Endpoint string
	UsePathStyle                     bool
	Staging                          Store
}

type s3API interface {
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

type S3Store struct {
	bucket, prefix string
	staging        Store
	client         s3API
}

var _ Backend = (*S3Store)(nil)

func NewS3Store(ctx context.Context, cfg S3Config) (*S3Store, error) {
	if cfg.Bucket == "" || cfg.Region == "" {
		return nil, errors.New("S3 bucket and region are required")
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.UsePathStyle = cfg.UsePathStyle
		if cfg.Endpoint != "" {
			options.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})
	return newS3Store(cfg, client)
}

func newS3Store(cfg S3Config, client s3API) (*S3Store, error) {
	prefix := strings.Trim(strings.ReplaceAll(cfg.Prefix, "\\", "/"), "/")
	if prefix != "" {
		if _, err := cleanRelative(prefix); err != nil {
			return nil, fmt.Errorf("invalid S3 prefix: %w", err)
		}
	}
	return &S3Store{bucket: cfg.Bucket, prefix: prefix, staging: cfg.Staging, client: client}, nil
}

func (s *S3Store) Init(ctx context.Context) error {
	if err := s.staging.Init(ctx); err != nil {
		return err
	}
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	if err != nil {
		return fmt.Errorf("access S3 bucket %q: %w", s.bucket, err)
	}
	return nil
}

func (s *S3Store) StageZIP(ctx context.Context, uploadID, title, entrypoint string, r io.Reader) (Staged, error) {
	return s.staging.StageZIP(ctx, uploadID, title, entrypoint, r)
}

func (s *S3Store) Staged(uploadID string, manifest Manifest, digest string, bytes int64) Staged {
	return s.staging.Staged(uploadID, manifest, digest, bytes)
}

func (s *S3Store) PublishUpload(ctx context.Context, staged Staged, artifactID string) (string, error) {
	artifactID, err := cleanRelative(artifactID)
	if err != nil {
		return "", fmt.Errorf("artifact ID: %w", err)
	}
	uploadID, err := cleanRelative(staged.UploadID)
	if err != nil || strings.Contains(uploadID, "/") {
		return "", errors.New("invalid upload ID")
	}
	objectDir := path.Join(artifactID, "objects", uploadID)
	filesRoot := filepath.Join(staged.Directory, "files")
	err = filepath.WalkDir(filesRoot, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(filesRoot, filename)
		if err != nil {
			return err
		}
		name, err := cleanRelative(filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		return s.putFile(ctx, path.Join(objectDir, "files", name), filename, mime.TypeByExtension(strings.ToLower(path.Ext(name))))
	})
	if err != nil {
		return "", err
	}
	manifestJSON, err := json.Marshal(staged.Manifest)
	if err != nil {
		return "", err
	}
	manifestJSON = append(manifestJSON, '\n')
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.key(path.Join(objectDir, "manifest.json"))),
		Body: bytes.NewReader(manifestJSON), ContentLength: aws.Int64(int64(len(manifestJSON))), ContentType: aws.String("application/json"),
	})
	if err != nil {
		return "", fmt.Errorf("upload manifest to S3: %w", err)
	}
	if err := s.staging.RemoveStage(staged.UploadID); err != nil {
		return "", err
	}
	return objectDir, nil
}

func (s *S3Store) putFile(ctx context.Context, objectName, filename, contentType string) (err error) {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer closeWithError(&err, "close staged file", file.Close)
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.key(objectName)), Body: file,
		ContentLength: aws.Int64(info.Size()), ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("upload %q to S3: %w", objectName, err)
	}
	return nil
}

func (s *S3Store) Open(ctx context.Context, objectDir, name string) (_ Object, err error) {
	objectDir, err = cleanRelative(objectDir)
	if err != nil {
		return Object{}, err
	}
	name, err = cleanRelative(name)
	if err != nil {
		return Object{}, err
	}
	key := s.key(path.Join(objectDir, "files", name))
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return Object{}, err
	}
	size := aws.ToInt64(out.ContentLength)
	if size < 0 || size > s.staging.MaxBundleBytes {
		return Object{}, errors.New("S3 object size exceeds configured bundle limit")
	}
	body := &s3ReadSeeker{ctx: ctx, client: s.client, bucket: s.bucket, key: key, size: size}
	return Object{Body: body, Size: size, LastModified: aws.ToTime(out.LastModified)}, nil
}

func (s *S3Store) RemoveStage(uploadID string) error { return s.staging.RemoveStage(uploadID) }
func (s *S3Store) CleanupStages(before time.Time) (int, error) {
	return s.staging.CleanupStages(before)
}

func (s *S3Store) key(name string) string {
	if s.prefix == "" {
		return name
	}
	return path.Join(s.prefix, name)
}

type s3ReadSeeker struct {
	ctx            context.Context
	client         s3API
	bucket, key    string
	size, position int64
	body           io.ReadCloser
	closed         bool
}

func (r *s3ReadSeeker) Read(p []byte) (int, error) {
	if r.closed {
		return 0, os.ErrClosed
	}
	if r.position >= r.size {
		return 0, io.EOF
	}
	if int64(len(p)) > r.size-r.position {
		p = p[:r.size-r.position]
	}
	if r.body == nil {
		input := &s3.GetObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(r.key)}
		if r.position > 0 {
			input.Range = aws.String(fmt.Sprintf("bytes=%d-", r.position))
		}
		out, err := r.client.GetObject(r.ctx, input)
		if err != nil {
			return 0, err
		}
		if length := aws.ToInt64(out.ContentLength); length != r.size-r.position {
			return 0, errors.Join(errors.New("S3 object length does not match metadata"), out.Body.Close())
		}
		r.body = out.Body
	}
	n, err := r.body.Read(p)
	r.position += int64(n)
	if err != nil {
		closeErr := r.body.Close()
		r.body = nil
		if errors.Is(err, io.EOF) && r.position < r.size {
			err = io.ErrUnexpectedEOF
		}
		if closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return n, err
	}
	return n, nil
}

func (r *s3ReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if r.closed {
		return 0, os.ErrClosed
	}
	var position int64
	switch whence {
	case io.SeekStart:
		position = offset
	case io.SeekCurrent:
		position = r.position + offset
	case io.SeekEnd:
		position = r.size + offset
	default:
		return 0, errors.New("invalid seek whence")
	}
	if position < 0 {
		return 0, errors.New("negative seek position")
	}
	if position == r.position {
		return position, nil
	}
	if r.body != nil {
		if err := r.body.Close(); err != nil {
			return r.position, err
		}
		r.body = nil
	}
	r.position = position
	return position, nil
}

func (r *s3ReadSeeker) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.body != nil {
		return r.body.Close()
	}
	return nil
}
