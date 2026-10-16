package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/kilo666mj/rendercase/internal/store"
	"github.com/kilo666mj/rendercase/internal/testdb"
)

func TestServiceUserVisibilityIntegration(t *testing.T) {
	db := testdb.New(t)
	ctx := t.Context()
	person := func(subject string) store.User {
		t.Helper()
		u, err := db.UpsertUser(ctx, subject, subject, subject+"@example.com", subject, false)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	owner, reader := person("cloudflare_access:owner"), person("cloudflare_access:reader")
	service, err := db.UpsertServiceUser(ctx, "service:example:publisher", "Service: publisher")
	if err != nil || service.Admin {
		t.Fatalf("service user = %+v, %v", service, err)
	}
	if again, err := db.UpsertServiceUser(ctx, "service:example:publisher", "Service: publisher"); err != nil || again.ID != service.ID {
		t.Fatalf("service user refresh = %+v, %v", again, err)
	}
	if _, err := db.UpsertServiceUser(ctx, reader.Subject, "hijack"); !errors.Is(err, store.ErrServiceSubjectConflict) {
		t.Fatalf("person converted to a service user: %v", err)
	}
	if _, err := db.UpsertUser(ctx, service.Subject, "person", "person@example.com", "Person", true); !errors.Is(err, store.ErrPersonSubjectConflict) {
		t.Fatalf("person took over a service user: %v", err)
	}
	publish := func(id string, by store.User) store.Artifact {
		t.Helper()
		if err := db.CreateUpload(ctx, store.Upload{ID: id, CreatedBy: by.ID, Title: id, Entrypoint: "index.html", TokenHash: []byte(id), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		a, _, err := db.CommitVersion(ctx, store.CommitInput{UploadID: id, UserID: by.ID, Title: id, Entrypoint: "index.html", ObjectDir: id, Manifest: []byte(`{}`), ManifestSHA256: id, ByteSize: 1, FileCount: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.SetArtifactVisibility(ctx, a.ID, by.ID, "authenticated"); err != nil {
			t.Fatal(err)
		}
		return a
	}
	listed := func(userID, artifactID string) bool {
		t.Helper()
		artifacts, err := db.ListArtifacts(ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range artifacts {
			if a.ID == artifactID {
				return true
			}
		}
		return false
	}

	peoples := publish("people-org-visible", owner)
	if _, err := db.ArtifactForUser(ctx, peoples.ID, reader.ID); err != nil || !listed(reader.ID, peoples.ID) {
		t.Fatalf("a person lost an authenticated artifact: %v", err)
	}
	if _, err := db.ArtifactForUser(ctx, peoples.ID, service.ID); !errors.Is(err, store.ErrNotFound) || listed(service.ID, peoples.ID) {
		t.Fatalf("service user read a person's authenticated artifact: %v", err)
	}
	if _, err := db.SetArtifactVisibility(ctx, peoples.ID, service.ID, "private"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("service user changed a person's artifact: %v", err)
	}

	own := publish("service-org-visible", service)
	if _, err := db.ArtifactForUser(ctx, own.ID, service.ID); err != nil || !listed(service.ID, own.ID) {
		t.Fatalf("service user lost its own artifact: %v", err)
	}
	if _, err := db.ArtifactForUser(ctx, own.ID, reader.ID); err != nil || !listed(reader.ID, own.ID) {
		t.Fatalf("people cannot read the service user's authenticated artifact: %v", err)
	}
}
