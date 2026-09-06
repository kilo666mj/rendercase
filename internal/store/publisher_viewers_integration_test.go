package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/kilo666mj/rendercase/internal/store"
	"github.com/kilo666mj/rendercase/internal/testdb"
)

func TestPublisherViewerGrantsIntegration(t *testing.T) {
	db := testdb.New(t)
	ctx := t.Context()
	user := func(subject string) store.User {
		t.Helper()
		u, err := db.UpsertUser(ctx, subject, subject, subject+"@example.com", subject, false)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	publisher, viewer, stranger := user("publisher"), user("viewer"), user("stranger")
	publish := func(id, artifactID, viewerSubject string) (store.Artifact, error) {
		t.Helper()
		if err := db.CreateUpload(ctx, store.Upload{ID: id, CreatedBy: publisher.ID, Title: id, Entrypoint: "index.html", TokenHash: []byte(id), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		a, _, err := db.CommitVersion(ctx, store.CommitInput{UploadID: id, UserID: publisher.ID, ArtifactID: artifactID, Title: id, Entrypoint: "index.html", ObjectDir: id, Manifest: []byte(`{}`), ManifestSHA256: id, ByteSize: 1, FileCount: 1, ViewerSubject: viewerSubject})
		return a, err
	}
	a, err := publish("first", "", viewer.Subject)
	if err != nil {
		t.Fatal(err)
	}
	if a.OwnerID != publisher.ID || a.Visibility != "private" {
		t.Fatalf("ownership/visibility changed: %+v", a)
	}
	visible, err := db.ArtifactForUser(ctx, a.ID, viewer.ID)
	if err != nil || visible.Role != "viewer" {
		t.Fatalf("viewer access: %+v, %v", visible, err)
	}
	if _, err := db.ArtifactForUser(ctx, a.ID, stranger.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stranger access: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE artifact_grants SET role='editor' WHERE artifact_id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := publish("second", a.ID, viewer.Subject); err != nil {
		t.Fatal(err)
	}
	visible, err = db.ArtifactForUser(ctx, a.ID, viewer.ID)
	if err != nil || visible.Role != "editor" || visible.LatestVersion != 2 {
		t.Fatalf("existing grant/version: %+v, %v", visible, err)
	}
	legacy, err := publish("legacy", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ArtifactForUser(ctx, legacy.ID, viewer.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unmapped publication visible: %v", err)
	}
	deleted, err := publish("deleted", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE artifacts SET deleted_at=now() WHERE id=$1`, deleted.ID); err != nil {
		t.Fatal(err)
	}
	mappings := map[string]string{publisher.Subject: viewer.Subject}
	n, err := db.BackfillPublisherViewers(ctx, mappings)
	if err != nil || n != 1 {
		t.Fatalf("backfill = %d, %v", n, err)
	}
	n, err = db.BackfillPublisherViewers(ctx, mappings)
	if err != nil || n != 0 {
		t.Fatalf("repeated backfill = %d, %v", n, err)
	}
	list, err := db.ListArtifacts(ctx, viewer.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("dashboard = %+v, %v", list, err)
	}
	if _, err := publish("missing", "", "unknown"); err == nil {
		t.Fatal("missing recipient accepted")
	}
	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE id='a_missing'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed publication left artifact: %d, %v", count, err)
	}
	var committed bool
	if err := db.Pool.QueryRow(ctx, `SELECT committed_at IS NOT NULL FROM upload_sessions WHERE id='missing'`).Scan(&committed); err != nil || committed {
		t.Fatalf("failed upload committed: %v, %v", committed, err)
	}
	// A later invalid mapping must roll back earlier grants in the same backfill.
	if _, err := db.Pool.Exec(ctx, `DELETE FROM artifact_grants WHERE artifact_id=$1`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BackfillPublisherViewers(ctx, map[string]string{"publisher": "viewer", "zzz": "unknown"}); err == nil {
		t.Fatal("invalid backfill recipient accepted")
	}
	if _, err := db.ArtifactForUser(ctx, legacy.ID, viewer.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("partial backfill committed: %v", err)
	}
	if _, err := db.BackfillPublisherViewers(ctx, nil); err != nil {
		t.Fatal(err)
	}
	visible, err = db.ArtifactForUser(ctx, a.ID, viewer.ID)
	if err != nil || visible.Role != "editor" {
		t.Fatalf("mapping removal revoked/downgraded existing grant: %+v %v", visible, err)
	}
}
