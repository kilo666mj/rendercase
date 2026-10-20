package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
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
	branding, err := db.Branding(ctx)
	if err != nil || branding.SiteName == "" {
		t.Fatalf("default branding = %+v, %v", branding, err)
	}
	theme := branding
	theme.ThemeName = "Integration theme"
	theme.SiteName = "Integration Co"
	if err := db.UpdateBranding(ctx, theme); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ActivateBrandingTheme(ctx, "Default"); err != nil {
		t.Fatal(err)
	}
	active, err := db.ActivateBrandingTheme(ctx, theme.ThemeName)
	if err != nil || active.SiteName != theme.SiteName {
		t.Fatalf("activate theme = %+v, %v", active, err)
	}
	if err := db.DeleteBrandingTheme(ctx, theme.ThemeName); !errors.Is(err, ErrActiveTheme) {
		t.Fatalf("delete active theme: %v", err)
	}
	if _, err := db.ActivateBrandingTheme(ctx, "Default"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteBrandingTheme(ctx, theme.ThemeName); err != nil {
		t.Fatalf("delete inactive theme: %v", err)
	}
	if err := db.DeleteBrandingTheme(ctx, theme.ThemeName); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing theme: %v", err)
	}
	if err := db.UpdateBranding(ctx, theme); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users(id,oidc_subject,username,email,display_name,is_admin) VALUES
		('u_owner','owner-sub','owner','owner@example.com','Owner',false),
		('u_viewer','viewer-sub','viewer','viewer@example.com','Viewer',false),
		('u_admin','admin-sub','admin','admin@example.com','Admin',true);
		INSERT INTO artifacts(id,owner_id,title,latest_version) VALUES('a_test','u_owner','Test artifact',1);
		INSERT INTO artifact_versions(artifact_id,version,title,entrypoint,object_dir,manifest,manifest_sha256,byte_size,file_count,created_by)
		VALUES('a_test',1,'Test artifact','index.html','a_test/objects/o1','{}','digest',1,1,'u_owner');
		INSERT INTO shares(id,artifact_id,version,created_by,token_hash) VALUES
		('s_one','a_test',1,'u_owner',decode('01','hex')),
		('s_two','a_test',1,'u_owner',decode('02','hex'));
		INSERT INTO shares(id,artifact_id,version,created_by,token_hash,view_limit) VALUES
		('s_limited','a_test',1,'u_owner',decode('03','hex'),2);
		INSERT INTO share_sessions(token_hash,share_id,expires_at) VALUES
		(decode('11','hex'),'s_one',now()+interval '1 hour'),
		(decode('12','hex'),'s_two',now()+interval '1 hour');`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ActivateBrandingTheme(context.Background(), "Default")
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM branding_themes WHERE name='Integration theme'`)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM artifacts WHERE id='a_test'; DELETE FROM users WHERE id IN ('u_owner','u_viewer','u_admin')`)
	})

	if _, err := db.ArtifactForUser(ctx, "a_test", "u_viewer"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private artifact accessible to another account: %v", err)
	}
	visible, err := db.SetArtifactVisibility(ctx, "a_test", "u_owner", "authenticated")
	if err != nil || visible.Visibility != "authenticated" {
		t.Fatalf("set authenticated visibility = %+v, %v", visible, err)
	}
	viewerArtifact, err := db.ArtifactForUser(ctx, "a_test", "u_viewer")
	if err != nil || viewerArtifact.Role != "viewer" || viewerArtifact.Visibility != "authenticated" {
		t.Fatalf("authenticated artifact for viewer = %+v, %v", viewerArtifact, err)
	}
	if _, err := db.SetArtifactVisibility(ctx, "a_test", "u_viewer", "private"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-owner changed visibility: %v", err)
	}

	artifacts, err := db.ListAdminArtifacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].OwnerEmail != "owner@example.com" || len(artifacts[0].Shares) != 3 {
		t.Fatalf("admin artifacts = %+v", artifacts)
	}
	limited, err := db.ExchangeShare(ctx, []byte{3}, []byte{0x13}, time.Now().Add(time.Hour))
	if err != nil || limited.ViewCount != 0 {
		t.Fatalf("exchange limited share = %+v, %v", limited, err)
	}
	for want := 1; want <= 2; want++ {
		viewed, viewErr := db.ShareBySession(ctx, []byte{0x13})
		if viewErr != nil || viewed.ViewCount != want {
			t.Fatalf("limited share view %d = %+v, %v", want, viewed, viewErr)
		}
	}
	if _, err := db.ShareBySession(ctx, []byte{0x13}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("limited share allowed a third view: %v", err)
	}
	if artifactID, err := db.AdminRevokeShare(ctx, "s_one"); err != nil || artifactID != "a_test" {
		t.Fatalf("revoke share = %q, %v", artifactID, err)
	}
	if artifactID, err := db.RevokeShare(ctx, "s_two", "u_owner"); err != nil || artifactID != "a_test" {
		t.Fatalf("owner revoke share = %q, %v", artifactID, err)
	}
	var ownerShareSessions int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM share_sessions WHERE share_id='s_two'`).Scan(&ownerShareSessions); err != nil {
		t.Fatal(err)
	}
	if ownerShareSessions != 0 {
		t.Fatalf("owner-revoked share retained %d sessions", ownerShareSessions)
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
