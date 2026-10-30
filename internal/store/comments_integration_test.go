package store_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kilo666mj/rendercase/internal/store"
	"github.com/kilo666mj/rendercase/internal/testdb"
)

func commentUser(t *testing.T, db *store.DB, name string) store.User {
	t.Helper()
	u, err := db.UpsertUser(t.Context(), name+"-subject", name, name+"@example.com", name, false)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func commentVersion(t *testing.T, db *store.DB, owner store.User, uploadID, artifactID string) store.Artifact {
	t.Helper()
	ctx := t.Context()
	if err := db.CreateUpload(ctx, store.Upload{ID: uploadID, CreatedBy: owner.ID, Title: uploadID, Entrypoint: "index.html", TokenHash: []byte(uploadID), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	a, _, err := db.CommitVersion(ctx, store.CommitInput{UploadID: uploadID, UserID: owner.ID, ArtifactID: artifactID, Title: uploadID, Entrypoint: "index.html", ObjectDir: uploadID, Manifest: []byte(`{}`), ManifestSHA256: uploadID, ByteSize: 1, FileCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func sameJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

func TestCommentThreadLifecycleIntegration(t *testing.T) {
	db := testdb.New(t)
	ctx := t.Context()
	owner, reviewer := commentUser(t, db, "owner"), commentUser(t, db, "reviewer")
	a := commentVersion(t, db, owner, "v1", "")
	anchor := `{"selector":"main > p:nth-of-type(1)","quote":{"exact":"Hello"},"point":{"x":0.5,"y":0.25}}`

	thread, err := db.CreateCommentThread(ctx, store.NewCommentThread{ThreadID: "ct_1", CommentID: "c_1", ArtifactID: a.ID, Version: 1, Anchor: json.RawMessage(anchor), UserID: reviewer.ID, Body: "Looks off"})
	if err != nil {
		t.Fatal(err)
	}
	if thread.Version != 1 || thread.CreatedBy.ID != reviewer.ID || thread.CreatedBy.DisplayName != "reviewer" || len(thread.Comments) != 1 || thread.Comments[0].Body != "Looks off" {
		t.Fatalf("created thread = %+v", thread)
	}
	sameJSON(t, thread.Anchor, anchor)

	if _, err := db.AddComment(ctx, "c_2", "ct_1", owner.ID, "Fixed in v2"); err != nil {
		t.Fatal(err)
	}
	edited, err := db.EditComment(ctx, "c_2", "Fixed in version 2")
	if err != nil || edited.Body != "Fixed in version 2" || edited.EditedAt == nil {
		t.Fatalf("edit = %+v, %v", edited, err)
	}

	open, closed := false, true
	threads, err := db.ListCommentThreads(ctx, a.ID, store.ThreadFilter{Version: 1, Resolved: &open})
	if err != nil || len(threads) != 1 || len(threads[0].Comments) != 2 || threads[0].Comments[0].ID != "c_1" || threads[0].Comments[1].ID != "c_2" {
		t.Fatalf("open threads = %+v, %v", threads, err)
	}

	resolved, err := db.SetThreadResolved(ctx, "ct_1", owner.ID, true)
	if err != nil || resolved.ResolvedAt == nil || resolved.ResolvedBy == nil || resolved.ResolvedBy.ID != owner.ID {
		t.Fatalf("resolve = %+v, %v", resolved, err)
	}
	for _, tc := range []struct {
		name   string
		filter *bool
		want   int
	}{{"open", &open, 0}, {"resolved", &closed, 1}, {"both", nil, 1}} {
		got, err := db.ListCommentThreads(ctx, a.ID, store.ThreadFilter{Resolved: tc.filter})
		if err != nil || len(got) != tc.want {
			t.Fatalf("filter %s: %d threads, %v", tc.name, len(got), err)
		}
	}
	reopened, err := db.SetThreadResolved(ctx, "ct_1", owner.ID, false)
	if err != nil || reopened.ResolvedAt != nil || reopened.ResolvedBy != nil {
		t.Fatalf("reopen = %+v, %v", reopened, err)
	}

	if err := db.DeleteComment(ctx, "c_1"); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.Comment(ctx, "c_1")
	if err != nil || deleted.DeletedAt == nil || deleted.Body != "" {
		t.Fatalf("deleted comment = %+v, %v", deleted, err)
	}
	if _, err := db.EditComment(ctx, "c_1", "again"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("edit deleted: %v", err)
	}
	if err := db.DeleteComment(ctx, "c_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("double delete: %v", err)
	}
	if got, err := db.CommentThread(ctx, "ct_1"); err != nil || len(got.Comments) != 2 {
		t.Fatalf("thread with one live comment = %+v, %v", got, err)
	}

	if err := db.DeleteComment(ctx, "c_2"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CommentThread(ctx, "ct_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("fully deleted thread visible: %v", err)
	}
	if got, err := db.ListCommentThreads(ctx, a.ID, store.ThreadFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("fully deleted thread listed: %+v, %v", got, err)
	}
}

func TestCommentThreadVersionsIntegration(t *testing.T) {
	db := testdb.New(t)
	ctx := t.Context()
	owner := commentUser(t, db, "owner")
	a := commentVersion(t, db, owner, "v1", "")
	commentVersion(t, db, owner, "v2", a.ID)
	for i, version := range []int{1, 2} {
		id := []string{"ct_a", "ct_b"}[i]
		if _, err := db.CreateCommentThread(ctx, store.NewCommentThread{ThreadID: id, CommentID: "c_" + id, ArtifactID: a.ID, Version: version, UserID: owner.ID, Body: "note"}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := db.ListCommentThreads(ctx, a.ID, store.ThreadFilter{Version: 2}); err != nil || len(got) != 1 || got[0].ID != "ct_b" || got[0].Anchor != nil {
		t.Fatalf("version 2 = %+v, %v", got, err)
	}
	if got, err := db.ListCommentThreads(ctx, a.ID, store.ThreadFilter{}); err != nil || len(got) != 2 || got[0].ID != "ct_a" {
		t.Fatalf("all versions = %+v, %v", got, err)
	}
	if _, err := db.CreateCommentThread(ctx, store.NewCommentThread{ThreadID: "ct_x", CommentID: "c_x", ArtifactID: a.ID, Version: 9, UserID: owner.ID, Body: "note"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing version: %v", err)
	}
}

func TestCommentsHiddenForDeletedArtifactIntegration(t *testing.T) {
	db := testdb.New(t)
	ctx := t.Context()
	owner := commentUser(t, db, "owner")
	a := commentVersion(t, db, owner, "v1", "")
	if _, err := db.CreateCommentThread(ctx, store.NewCommentThread{ThreadID: "ct_1", CommentID: "c_1", ArtifactID: a.ID, Version: 1, UserID: owner.ID, Body: "note"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE artifacts SET deleted_at=now() WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CommentThread(ctx, "ct_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("thread: %v", err)
	}
	if _, err := db.Comment(ctx, "c_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("comment: %v", err)
	}
	if _, err := db.AddComment(ctx, "c_2", "ct_1", owner.ID, "reply"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reply: %v", err)
	}
	if got, err := db.ListCommentThreads(ctx, a.ID, store.ThreadFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("list: %+v, %v", got, err)
	}
}

func TestCommentLimitIntegration(t *testing.T) {
	db := testdb.New(t)
	ctx := t.Context()
	owner := commentUser(t, db, "owner")
	a := commentVersion(t, db, owner, "v1", "")
	if _, err := db.CreateCommentThread(ctx, store.NewCommentThread{ThreadID: "ct_1", CommentID: "c_1", ArtifactID: a.ID, Version: 1, UserID: owner.ID, Body: "note"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO comments(id,thread_id,author_id,body) SELECT 'bulk_'||g,'ct_1',$1,'x' FROM generate_series(2,$2) g`, owner.ID, store.MaxCommentsPerArtifact); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddComment(ctx, "c_over", "ct_1", owner.ID, "one too many"); !errors.Is(err, store.ErrCommentLimit) {
		t.Fatalf("reply over limit: %v", err)
	}
	if _, err := db.CreateCommentThread(ctx, store.NewCommentThread{ThreadID: "ct_2", CommentID: "c_2", ArtifactID: a.ID, Version: 1, UserID: owner.ID, Body: "note"}); !errors.Is(err, store.ErrCommentLimit) {
		t.Fatalf("thread over limit: %v", err)
	}
}

// storeSnapshot captures every comment and thread row so a test can prove a
// rejected call changed nothing.
func storeSnapshot(t *testing.T, db *store.DB) string {
	t.Helper()
	var s string
	err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT coalesce(string_agg(row_to_json(c)::text,'|' ORDER BY c.id),'') FROM comments c)
		|| ' # ' || (SELECT coalesce(string_agg(row_to_json(t)::text,'|' ORDER BY t.id),'') FROM comment_threads t)`).Scan(&s)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHiddenCommentsAreImmutableIntegration(t *testing.T) {
	db := testdb.New(t)
	ctx := t.Context()
	owner := commentUser(t, db, "owner")
	a := commentVersion(t, db, owner, "v1", "")
	if _, err := db.CreateCommentThread(ctx, store.NewCommentThread{ThreadID: "ct_1", CommentID: "c_1", ArtifactID: a.ID, Version: 1, UserID: owner.ID, Body: "note"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE artifacts SET deleted_at=now() WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	before := storeSnapshot(t, db)
	if _, err := db.EditComment(ctx, "c_1", "changed"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("edit hidden: %v", err)
	}
	if err := db.DeleteComment(ctx, "c_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete hidden: %v", err)
	}
	if _, err := db.SetThreadResolved(ctx, "ct_1", owner.ID, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("resolve hidden: %v", err)
	}
	if after := storeSnapshot(t, db); after != before {
		t.Fatalf("hidden rows changed:\nbefore %s\nafter  %s", before, after)
	}
}

func TestFullyDeletedThreadCannotBeRevivedIntegration(t *testing.T) {
	db := testdb.New(t)
	ctx := t.Context()
	owner := commentUser(t, db, "owner")
	a := commentVersion(t, db, owner, "v1", "")
	if _, err := db.CreateCommentThread(ctx, store.NewCommentThread{ThreadID: "ct_1", CommentID: "c_1", ArtifactID: a.ID, Version: 1, UserID: owner.ID, Body: "note"}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteComment(ctx, "c_1"); err != nil {
		t.Fatal(err)
	}
	before := storeSnapshot(t, db)
	if _, err := db.AddComment(ctx, "c_2", "ct_1", owner.ID, "revive"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reply to hidden thread: %v", err)
	}
	if _, err := db.SetThreadResolved(ctx, "ct_1", owner.ID, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("resolve hidden thread: %v", err)
	}
	if after := storeSnapshot(t, db); after != before {
		t.Fatalf("hidden thread changed:\nbefore %s\nafter  %s", before, after)
	}
}
