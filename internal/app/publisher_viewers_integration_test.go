package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kilo666mj/mcpkit/mcpkittest"
	"github.com/kilo666mj/rendercase/internal/blob"
	"github.com/kilo666mj/rendercase/internal/config"
	"github.com/kilo666mj/rendercase/internal/store"
	"github.com/kilo666mj/rendercase/internal/testdb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMappedPublisherVisibleInBrowserIntegration(t *testing.T) {
	db := testdb.New(t)
	ctx := t.Context()
	publisher, err := db.UpsertUser(ctx, "client-publisher", "publisher", "", "Publisher", false)
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := db.UpsertUser(ctx, "browser-subject", "viewer", "viewer@example.com", "Viewer", false)
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := db.UpsertUser(ctx, "other-subject", "other", "other@example.com", "Other", false)
	if err != nil {
		t.Fatal(err)
	}
	publicURL, err := url.Parse("https://rendercase.example.com")
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := template.ParseFS(webFS, "web/*.html")
	if err != nil {
		t.Fatal(err)
	}
	blobs := blob.Store{Root: t.TempDir(), MaxBundleBytes: 1 << 20, MaxFiles: 10}
	if err := blobs.Init(ctx); err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db, blobs: blobs, tpl: tpl, log: slog.Default(), cfg: config.Config{PublicURL: publicURL, MaxBundleBytes: 1 << 20, MaxFiles: 10, UploadTTL: time.Hour, PublisherViewers: map[string]string{publisher.Subject: viewer.Subject}}}
	var bundle bytes.Buffer
	zw := zip.NewWriter(&bundle)
	file, err := zw.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("<!doctype html><title>Example</title>Example")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	session := mcpkittest.Connect(t, s.newMCPServer(publisher))
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "rendercase_publish", Arguments: map[string]any{"title": "Mapped artifact", "bundle_base64": base64.StdEncoding.EncodeToString(bundle.Bytes())}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("publish failed: %+v", result)
	}
	// Render the browser dashboard directly, without any MCP list call.
	dashboard := func(user store.User) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, publicURL.String(), nil)
		req = req.WithContext(context.WithValue(req.Context(), userContextKey{}, user))
		response := httptest.NewRecorder()
		s.index(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("dashboard status %d: %s", response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	if !strings.Contains(dashboard(viewer), "Mapped artifact") {
		t.Fatal("mapped publication missing from browser")
	}
	if strings.Contains(dashboard(stranger), "Mapped artifact") {
		t.Fatal("private artifact exposed to unrelated user")
	}

	// Exercise the staged-upload REST path used by API/CLI clients as well.
	request := func(method, path, body string) *http.Request {
		r := httptest.NewRequest(method, publicURL.String()+path, strings.NewReader(body))
		return r.WithContext(context.WithValue(r.Context(), userContextKey{}, publisher))
	}
	created := httptest.NewRecorder()
	s.createUpload(created, request(http.MethodPost, "/api/v1/artifacts/uploads", `{"title":"Staged artifact"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var upload struct {
		ID    string `json:"upload_id"`
		Token string `json:"upload_token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	put := httptest.NewRequest(http.MethodPut, "/upload/"+upload.ID, bytes.NewReader(bundle.Bytes()))
	put.SetPathValue("upload", upload.ID)
	put.Header.Set("X-Rendercase-Upload-Token", upload.Token)
	staged := httptest.NewRecorder()
	s.putUpload(staged, put)
	if staged.Code < 200 || staged.Code >= 300 {
		t.Fatalf("stage: %d %s", staged.Code, staged.Body.String())
	}
	body, err := json.Marshal(map[string]string{"upload_token": upload.Token})
	if err != nil {
		t.Fatal(err)
	}
	commit := request(http.MethodPost, "/api/v1/uploads/"+upload.ID+"/commit", string(body))
	commit.SetPathValue("upload", upload.ID)
	committed := httptest.NewRecorder()
	s.commitUpload(committed, commit)
	if committed.Code < 200 || committed.Code >= 300 {
		t.Fatalf("commit: %d %s", committed.Code, committed.Body.String())
	}
	if !strings.Contains(dashboard(viewer), "Staged artifact") {
		t.Fatal("staged publication missing from browser")
	}
	if strings.Contains(dashboard(stranger), "Staged artifact") {
		t.Fatal("staged publication exposed to unrelated user")
	}
	retry := httptest.NewRecorder()
	commit = request(http.MethodPost, "/api/v1/uploads/"+upload.ID+"/commit", string(body))
	commit.SetPathValue("upload", upload.ID)
	s.commitUpload(retry, commit)
	if retry.Code != committed.Code {
		t.Fatalf("retry: %d %s", retry.Code, retry.Body.String())
	}
}
