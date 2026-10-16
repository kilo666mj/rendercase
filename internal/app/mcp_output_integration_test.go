package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/kilo666mj/rendercase/internal/blob"
	"github.com/kilo666mj/rendercase/internal/config"
	"github.com/kilo666mj/rendercase/internal/testdb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.michaelspost.com/mcpkit/mcpkittest"
)

func TestMCPOutputsUseSnakeCaseIntegration(t *testing.T) {
	db := testdb.New(t)
	ctx := t.Context()
	user, err := db.UpsertUser(ctx, "publisher-subject", "publisher", "", "Publisher", false)
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
	s := &Server{db: db, blobs: blobs, tpl: tpl, log: slog.Default(), cfg: config.Config{PublicURL: publicURL, MaxBundleBytes: 1 << 20, MaxFiles: 10, UploadTTL: time.Hour}}
	session := mcpkittest.Connect(t, s.newMCPServer(user))
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s: %v %+v", name, err, result)
		}
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	keys := func(where string, v any, want ...string) {
		t.Helper()
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%s: not an object: %#v", where, v)
		}
		var got []string
		for k := range m {
			got = append(got, k)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("%s keys = %v, want %v", where, got, want)
		}
	}
	artifactKeys := []string{"id", "owner_id", "slug", "title", "visibility", "latest_version", "created_at", "updated_at", "role"}
	versionKeys := []string{"artifact_id", "title", "entrypoint", "object_dir", "manifest_sha256", "version", "file_count", "byte_size", "manifest", "created_by", "created_at"}

	created := call("rendercase_create_upload", map[string]any{"title": "Keys"})
	keys("create_upload", created, "upload_id", "upload_token", "upload_url", "expires_at")

	var bundle bytes.Buffer
	zw := zip.NewWriter(&bundle)
	file, err := zw.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("<!doctype html><title>Keys</title>Keys")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	id, token := created["upload_id"].(string), created["upload_token"].(string)
	put := httptest.NewRequest(http.MethodPut, "/upload/"+id, bytes.NewReader(bundle.Bytes()))
	put.SetPathValue("upload", id)
	put.Header.Set("X-Rendercase-Upload-Token", token)
	staged := httptest.NewRecorder()
	s.putUpload(staged, put)
	if staged.Code < 200 || staged.Code >= 300 {
		t.Fatalf("stage: %d %s", staged.Code, staged.Body.String())
	}

	committed := call("rendercase_commit_upload", map[string]any{"upload_id": id, "upload_token": token})
	keys("commit_upload", committed, "artifact", "version", "url")
	keys("commit_upload.artifact", committed["artifact"], artifactKeys...)
	keys("commit_upload.version", committed["version"], versionKeys...)
	artifactID := committed["artifact"].(map[string]any)["id"].(string)

	got := call("rendercase_get", map[string]any{"artifact_id": artifactID})
	keys("get.artifact", got["artifact"], artifactKeys...)
	keys("get.version", got["version"], versionKeys...)
	listed := call("rendercase_list", map[string]any{})
	keys("list.artifacts[0]", listed["artifacts"].([]any)[0], artifactKeys...)
	keys("set_visibility", call("rendercase_set_visibility", map[string]any{"artifact_id": artifactID, "visibility": "private"}), artifactKeys...)
	keys("share", call("rendercase_share", map[string]any{"artifact_id": artifactID}), "share_id", "url")
}
