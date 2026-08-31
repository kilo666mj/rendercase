package blob

import (
	"context"
	"io"
	"time"
)

type Object struct {
	Body         ReadSeekCloser
	Size         int64
	LastModified time.Time
}

type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

type Backend interface {
	Init(context.Context) error
	StageZIP(context.Context, string, string, string, io.Reader) (Staged, error)
	Staged(string, Manifest, string, int64) Staged
	PublishUpload(context.Context, Staged, string) (string, error)
	Open(context.Context, string, string) (Object, error)
	RemoveStage(string) error
	CleanupStages(time.Time) (int, error)
}
