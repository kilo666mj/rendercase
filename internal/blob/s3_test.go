package blob

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type memoryS3 struct {
	objects    map[string][]byte
	content    map[string]string
	headBucket bool
}

func (m *memoryS3) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	m.headBucket = true
	return &s3.HeadBucketOutput{}, nil
}

func (m *memoryS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	m.objects[aws.ToString(in.Key)] = body
	m.content[aws.ToString(in.Key)] = aws.ToString(in.ContentType)
	return &s3.PutObjectOutput{}, nil
}

func (m *memoryS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	body := m.objects[aws.ToString(in.Key)]
	now := time.Now()
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body)), ContentLength: aws.Int64(int64(len(body))), LastModified: &now}, nil
}

func TestS3StoreStagesPublishesAndReads(t *testing.T) {
	client := &memoryS3{objects: make(map[string][]byte), content: make(map[string]string)}
	store, err := newS3Store(S3Config{
		Bucket: "artifacts", Prefix: "rendercase/test", Region: "us-east-1",
		Staging: Store{Root: t.TempDir(), MaxBundleBytes: 1024, MaxFiles: 10},
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if !client.headBucket {
		t.Fatal("S3 bucket was not checked")
	}
	staged, err := store.StageZIP(ctx, "upload1", "Example", "index.html", bytes.NewReader(zipBytes(t, map[string]string{
		"index.html": "<h1>Hello</h1>", "assets/app.js": "console.log('ok')",
	})))
	if err != nil {
		t.Fatal(err)
	}
	objectDir, err := store.PublishUpload(ctx, staged, "a_example")
	if err != nil {
		t.Fatal(err)
	}
	if objectDir != "a_example/objects/upload1" {
		t.Fatalf("object directory = %q", objectDir)
	}
	key := "rendercase/test/a_example/objects/upload1/files/index.html"
	if string(client.objects[key]) != "<h1>Hello</h1>" || client.content[key] != "text/html; charset=utf-8" {
		t.Fatalf("stored object = %q (%q)", client.objects[key], client.content[key])
	}
	if _, ok := client.objects["rendercase/test/a_example/objects/upload1/manifest.json"]; !ok {
		t.Fatal("manifest was not stored")
	}
	object, err := store.Open(ctx, objectDir, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer object.Body.Close()
	got, err := io.ReadAll(object.Body)
	if err != nil || string(got) != "<h1>Hello</h1>" || object.Size != int64(len(got)) {
		t.Fatalf("opened object = %q, size %d, err %v", got, object.Size, err)
	}
	if _, err := store.Open(ctx, objectDir, "../secret"); err == nil {
		t.Fatal("S3 path traversal accepted")
	}
}

func TestS3StoreRejectsUnsafePrefix(t *testing.T) {
	client := &memoryS3{objects: make(map[string][]byte), content: make(map[string]string)}
	for _, prefix := range []string{"../escape", "safe/../../escape", string([]byte{'b', 'a', 'd', 0})} {
		if _, err := newS3Store(S3Config{Bucket: "bucket", Region: "region", Prefix: prefix}, client); err == nil {
			t.Fatalf("unsafe prefix %q accepted", strings.ReplaceAll(prefix, "\x00", "\\0"))
		}
	}
}
