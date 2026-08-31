package store

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestAdminArtifactLifecycleIntegration(t *testing.T) {
	databaseURL := os.Getenv("RENDERCASE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RENDERCASE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users(id,oidc_subject,username,email,display_name,is_admin) VALUES
		('u_owner','owner-sub','owner','owner@example.com','Owner',false),
		('u_admin','admin-sub','admin','admin@example.com','Admin',true);
		INSERT INTO artifacts(id,owner_id,title,latest_version) VALUES('a_test','u_owner','Test artifact',1);
		INSERT INTO artifact_versions(artifact_id,version,title,entrypoint,object_dir,manifest,manifest_sha256,byte_size,file_count,created_by)
		VALUES('a_test',1,'Test artifact','index.html','a_test/objects/o1','{}','digest',1,1,'u_owner');
		INSERT INTO shares(id,artifact_id,version,created_by,token_hash) VALUES
		('s_one','a_test',1,'u_owner',decode('01','hex')),
		('s_two','a_test',1,'u_owner',decode('02','hex'));
		INSERT INTO share_sessions(token_hash,share_id,expires_at) VALUES
		(decode('11','hex'),'s_one',now()+interval '1 hour'),
		(decode('12','hex'),'s_two',now()+interval '1 hour');`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM artifacts WHERE id='a_test'; DELETE FROM users WHERE id IN ('u_owner','u_admin')`)
	})

	artifacts, err := db.ListAdminArtifacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].OwnerEmail != "owner@example.com" || len(artifacts[0].Shares) != 2 {
		t.Fatalf("admin artifacts = %+v", artifacts)
	}
	if artifactID, err := db.AdminRevokeShare(ctx, "s_one"); err != nil || artifactID != "a_test" {
		t.Fatalf("revoke share = %q, %v", artifactID, err)
	}
	removed, err := db.AdminDeleteArtifact(ctx, "a_test")
	if err != nil || removed.OwnerID != "u_owner" {
		t.Fatalf("delete artifact = %+v, %v", removed, err)
	}
	if _, err := db.Version(ctx, "a_test", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted artifact version remains accessible: %v", err)
	}
	var activeShares, shareSessions int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM shares WHERE artifact_id='a_test' AND revoked_at IS NULL`).Scan(&activeShares); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM share_sessions ss JOIN shares s ON s.id=ss.share_id WHERE s.artifact_id='a_test'`).Scan(&shareSessions); err != nil {
		t.Fatal(err)
	}
	if activeShares != 0 || shareSessions != 0 {
		t.Fatalf("after deletion: active shares=%d share sessions=%d", activeShares, shareSessions)
	}
}
