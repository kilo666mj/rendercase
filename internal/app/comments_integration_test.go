package app

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kilo666mj/rendercase/internal/blob"
	"github.com/kilo666mj/rendercase/internal/config"
	"github.com/kilo666mj/rendercase/internal/securetoken"
	"github.com/kilo666mj/rendercase/internal/store"
	"github.com/kilo666mj/rendercase/internal/testdb"
)

func testCommentServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	db := testdb.New(t)
	blobs := blob.Store{Root: t.TempDir(), MaxBundleBytes: 8 << 20, MaxFiles: 10}
	if err := blobs.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db, blobs: blobs, log: slog.Default(), mcp: http.NotFoundHandler(), cfg: config.Config{
		PublicURL:       mustParseURL(t, "https://rendercase.example/"),
		ContentURL:      mustParseURL(t, "https://content.rendercase.example/"),
		CookieSecret:    []byte("comment-test-secret-0123456789abcdef"),
		MaxBundleBytes:  8 << 20,
		MaxFiles:        10,
		UploadTTL:       time.Hour,
		ViewerTicketTTL: time.Minute,
	}}
	return s, db
}

func zipBundle(t *testing.T, files map[string]string) string {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

type commentFixture struct {
	t                               *testing.T
	s                               *Server
	db                              *store.DB
	artifactID                      string
	owner, editor, viewer, stranger store.User
	sessions                        map[string]string
}

func newCommentFixture(t *testing.T) *commentFixture {
	t.Helper()
	s, db := testCommentServer(t)
	ctx := t.Context()
	f := &commentFixture{t: t, s: s, db: db, sessions: map[string]string{}}
	user := func(name string) store.User {
		u, err := db.UpsertUser(ctx, name+"-subject", name, name+"@example.com", strings.ToUpper(name[:1])+name[1:], false)
		if err != nil {
			t.Fatal(err)
		}
		token := "session-" + u.ID
		if err := db.CreateSession(ctx, securetoken.Hash(token), u.ID, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		f.sessions[u.ID] = token
		return u
	}
	f.owner, f.editor, f.viewer, f.stranger = user("owner"), user("editor"), user("viewer"), user("stranger")
	out, err := s.publishBundleForUser(ctx, f.owner, publishInput{Title: "Commented", BundleBase64: zipBundle(t, map[string]string{"index.html": "<!doctype html><title>v1</title><p>Hello</p>"})})
	if err != nil {
		t.Fatal(err)
	}
	f.artifactID = out.Artifact.ID
	if _, err := db.Pool.Exec(ctx, `INSERT INTO artifact_grants(artifact_id,user_id,role) VALUES($1,$2,'editor'),($1,$3,'viewer')`, f.artifactID, f.editor.ID, f.viewer.ID); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *commentFixture) publishVersion() {
	f.t.Helper()
	if _, err := f.s.publishBundleForUser(f.t.Context(), f.owner, publishInput{Title: "Commented", ArtifactID: f.artifactID, BundleBase64: zipBundle(f.t, map[string]string{"index.html": "<!doctype html><title>v2</title><p>Hello again</p>"})}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *commentFixture) doRaw(user *store.User, method, target string, body []byte) (int, map[string]any) {
	f.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, "https://rendercase.example"+target, reader)
	req.Header.Set("Origin", "https://rendercase.example")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != nil {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.sessions[user.ID]})
	}
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			f.t.Fatalf("%s %s: decode %q: %v", method, target, rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

func (f *commentFixture) do(user *store.User, method, target string, body any) (int, map[string]any) {
	f.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			f.t.Fatal(err)
		}
	}
	return f.doRaw(user, method, target, raw)
}

func (f *commentFixture) threadsPath() string {
	return "/api/v1/artifacts/" + f.artifactID + "/threads"
}

func expectStatus(t *testing.T, what string, got, want int, out map[string]any) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: status %d, want %d (%v)", what, got, want, out)
	}
}

func TestCommentAPIPermissionsIntegration(t *testing.T) {
	f := newCommentFixture(t)
	anchor := map[string]any{"selector": "p:nth-of-type(1)", "quote": map[string]any{"exact": "Hello"}}
	code, out := f.do(&f.viewer, http.MethodPost, f.threadsPath(), map[string]any{"body": "Hello is misspelled", "anchor": anchor})
	expectStatus(t, "viewer creates thread", code, http.StatusCreated, out)
	thread := out["thread"].(map[string]any)
	threadID := thread["id"].(string)
	if thread["version"].(float64) != 1 || thread["anchor"].(map[string]any)["selector"] != "p:nth-of-type(1)" {
		t.Fatalf("thread = %v", thread)
	}
	root := thread["comments"].([]any)[0].(map[string]any)
	rootID := root["id"].(string)
	if root["can_edit"] != true || root["author"].(map[string]any)["display_name"] != "Viewer" {
		t.Fatalf("root comment = %v", root)
	}

	code, out = f.do(&f.stranger, http.MethodGet, f.threadsPath(), nil)
	expectStatus(t, "stranger lists", code, http.StatusNotFound, out)
	code, out = f.do(&f.stranger, http.MethodPost, "/api/v1/threads/"+threadID+"/comments", map[string]any{"body": "hi"})
	expectStatus(t, "stranger replies", code, http.StatusNotFound, out)
	code, out = f.do(nil, http.MethodGet, f.threadsPath(), nil)
	expectStatus(t, "anonymous lists", code, http.StatusUnauthorized, out)

	shareOnly := httptest.NewRequest(http.MethodGet, "https://rendercase.example"+f.threadsPath(), nil)
	shareOnly.AddCookie(&http.Cookie{Name: shareCookieName, Value: "share-session"})
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, shareOnly)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("share session lists: %d", rec.Code)
	}

	code, out = f.do(&f.editor, http.MethodPost, "/api/v1/threads/"+threadID+"/comments", map[string]any{"body": "Will fix"})
	expectStatus(t, "editor replies", code, http.StatusCreated, out)
	replyID := out["comment"].(map[string]any)["id"].(string)

	code, out = f.do(&f.owner, http.MethodPatch, "/api/v1/comments/"+rootID, map[string]any{"body": "rewritten"})
	expectStatus(t, "owner edits viewer comment", code, http.StatusForbidden, out)
	code, out = f.do(&f.viewer, http.MethodPatch, "/api/v1/comments/"+rootID, map[string]any{"body": "Hello is misspelled here"})
	expectStatus(t, "author edits", code, http.StatusOK, out)
	if out["comment"].(map[string]any)["edited_at"] == nil {
		t.Fatalf("edited comment = %v", out)
	}

	code, out = f.do(&f.viewer, http.MethodDelete, "/api/v1/comments/"+replyID, nil)
	expectStatus(t, "viewer deletes editor reply", code, http.StatusForbidden, out)
	code, out = f.do(&f.owner, http.MethodDelete, "/api/v1/comments/"+replyID, nil)
	expectStatus(t, "owner moderates", code, http.StatusNoContent, out)

	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE artifacts SET visibility='authenticated' WHERE id=$1`, f.artifactID); err != nil {
		t.Fatal(err)
	}
	code, out = f.do(&f.stranger, http.MethodGet, f.threadsPath(), nil)
	expectStatus(t, "signed-in user on authenticated artifact lists", code, http.StatusOK, out)
	if out["threads"].([]any)[0].(map[string]any)["can_resolve"] != false {
		t.Fatalf("stranger can resolve: %v", out)
	}
	resolution := "/api/v1/threads/" + threadID + "/resolution"
	code, out = f.do(&f.stranger, http.MethodPut, resolution, map[string]any{"resolved": true})
	expectStatus(t, "non-author viewer resolves", code, http.StatusForbidden, out)
	code, out = f.do(&f.viewer, http.MethodPut, resolution, map[string]any{"resolved": true})
	expectStatus(t, "author resolves", code, http.StatusOK, out)
	if out["thread"].(map[string]any)["resolved_at"] == nil {
		t.Fatalf("resolved thread = %v", out)
	}
	for query, want := range map[string]int{"": 0, "?resolved=true": 1, "?resolved=all": 1} {
		code, out = f.do(&f.owner, http.MethodGet, f.threadsPath()+query, nil)
		expectStatus(t, "list "+query, code, http.StatusOK, out)
		if got := len(out["threads"].([]any)); got != want {
			t.Fatalf("list %q: %d threads, want %d", query, got, want)
		}
	}
	code, out = f.do(&f.editor, http.MethodPut, resolution, map[string]any{"resolved": false})
	expectStatus(t, "editor reopens", code, http.StatusOK, out)
	code, out = f.do(&f.owner, http.MethodPut, resolution, map[string]any{})
	expectStatus(t, "missing resolved", code, http.StatusBadRequest, out)
}

func TestCommentAPIValidationIntegration(t *testing.T) {
	f := newCommentFixture(t)
	for name, body := range map[string]map[string]any{
		"empty body":       {"body": "   "},
		"nul body":         {"body": "a\u0000b"},
		"long body":        {"body": strings.Repeat("a", maxCommentRunes+1)},
		"unknown anchor":   {"body": "x", "anchor": map[string]any{"selector": "p", "extra": 1}},
		"point range":      {"body": "x", "anchor": map[string]any{"selector": "p", "point": map[string]any{"x": 1.5, "y": 0}}},
		"selector length":  {"body": "x", "anchor": map[string]any{"selector": strings.Repeat("a", 513)}},
		"negative version": {"body": "x", "version": -1},
		"huge version":     {"body": "x", "version": 3000000000},
		"unknown field":    {"body": "x", "colour": "red"},
	} {
		code, out := f.do(&f.viewer, http.MethodPost, f.threadsPath(), body)
		expectStatus(t, name, code, http.StatusBadRequest, out)
	}
	code, out := f.do(&f.viewer, http.MethodPost, f.threadsPath(), map[string]any{"body": strings.Repeat("é", maxCommentRunes)})
	expectStatus(t, "10000 two-byte characters", code, http.StatusCreated, out)
	code, out = f.doRaw(&f.viewer, http.MethodPost, f.threadsPath(), []byte(`{"body":"`+strings.Repeat("a", maxCommentRequestBytes)+`"}`))
	expectStatus(t, "oversized request", code, http.StatusBadRequest, out)
	code, out = f.do(&f.viewer, http.MethodPost, f.threadsPath(), map[string]any{"body": "x", "version": 99})
	expectStatus(t, "missing version", code, http.StatusNotFound, out)
	for _, query := range []string{"?version=abc", "?version=0", "?version=3000000000", "?resolved=maybe"} {
		code, out = f.do(&f.viewer, http.MethodGet, f.threadsPath()+query, nil)
		expectStatus(t, "query "+query, code, http.StatusBadRequest, out)
	}
}

func TestCommentAPIVersionsAndLifecycleIntegration(t *testing.T) {
	f := newCommentFixture(t)
	code, out := f.do(&f.viewer, http.MethodPost, f.threadsPath(), map[string]any{"body": "on v1"})
	expectStatus(t, "thread on v1", code, http.StatusCreated, out)
	v1Root := out["thread"].(map[string]any)["comments"].([]any)[0].(map[string]any)["id"].(string)
	f.publishVersion()

	for query, want := range map[string]int{"": 0, "?version=all": 1, "?version=1": 1, "?version=2": 0} {
		code, out = f.do(&f.viewer, http.MethodGet, f.threadsPath()+query, nil)
		expectStatus(t, "list "+query, code, http.StatusOK, out)
		if got := len(out["threads"].([]any)); got != want {
			t.Fatalf("list %q: %d threads, want %d", query, got, want)
		}
	}
	code, out = f.do(&f.viewer, http.MethodPost, f.threadsPath(), map[string]any{"body": "defaults to latest"})
	expectStatus(t, "thread without version", code, http.StatusCreated, out)
	v2 := out["thread"].(map[string]any)
	if v2["version"].(float64) != 2 {
		t.Fatalf("default version = %v", v2["version"])
	}
	v2Thread := v2["id"].(string)
	v2Root := v2["comments"].([]any)[0].(map[string]any)["id"].(string)

	code, out = f.do(&f.viewer, http.MethodDelete, "/api/v1/comments/"+v2Root, nil)
	expectStatus(t, "delete only comment", code, http.StatusNoContent, out)
	code, out = f.do(&f.owner, http.MethodPost, "/api/v1/threads/"+v2Thread+"/comments", map[string]any{"body": "too late"})
	expectStatus(t, "reply to emptied thread", code, http.StatusNotFound, out)

	if _, err := f.db.Pool.Exec(t.Context(), `DELETE FROM artifact_grants WHERE artifact_id=$1 AND user_id=$2`, f.artifactID, f.viewer.ID); err != nil {
		t.Fatal(err)
	}
	code, out = f.do(&f.viewer, http.MethodPatch, "/api/v1/comments/"+v1Root, map[string]any{"body": "still mine?"})
	expectStatus(t, "edit after losing access", code, http.StatusNotFound, out)
	code, out = f.do(&f.viewer, http.MethodDelete, "/api/v1/comments/"+v1Root, nil)
	expectStatus(t, "delete after losing access", code, http.StatusNotFound, out)
	code, out = f.do(&f.viewer, http.MethodGet, f.threadsPath()+"?version=all", nil)
	expectStatus(t, "list after losing access", code, http.StatusNotFound, out)
}

func TestCommentAPIAuditOmitsBodiesIntegration(t *testing.T) {
	f := newCommentFixture(t)
	secret := "audit-must-not-see-this"
	code, out := f.do(&f.viewer, http.MethodPost, f.threadsPath(), map[string]any{"body": secret})
	expectStatus(t, "create", code, http.StatusCreated, out)
	thread := out["thread"].(map[string]any)
	threadID := thread["id"].(string)
	rootID := thread["comments"].([]any)[0].(map[string]any)["id"].(string)
	f.do(&f.owner, http.MethodPost, "/api/v1/threads/"+threadID+"/comments", map[string]any{"body": secret})
	f.do(&f.viewer, http.MethodPatch, "/api/v1/comments/"+rootID, map[string]any{"body": secret + " edited"})
	f.do(&f.owner, http.MethodPut, "/api/v1/threads/"+threadID+"/resolution", map[string]any{"resolved": true})
	f.do(&f.owner, http.MethodPut, "/api/v1/threads/"+threadID+"/resolution", map[string]any{"resolved": false})
	f.do(&f.viewer, http.MethodDelete, "/api/v1/comments/"+rootID, nil)

	rows, err := f.db.Pool.Query(t.Context(), `SELECT action,details::text FROM audit_events WHERE action LIKE 'comment.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action, details string
		if err := rows.Scan(&action, &details); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(details, secret) {
			t.Fatalf("%s audit details contain the comment body: %s", action, details)
		}
		actions = append(actions, action)
	}
	want := []string{"comment.thread.create", "comment.create", "comment.edit", "comment.thread.resolve", "comment.thread.reopen", "comment.delete"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("audit actions = %v, want %v", actions, want)
	}
}

func TestCommentAPILimitIntegration(t *testing.T) {
	f := newCommentFixture(t)
	code, out := f.do(&f.viewer, http.MethodPost, f.threadsPath(), map[string]any{"body": "first"})
	expectStatus(t, "create", code, http.StatusCreated, out)
	threadID := out["thread"].(map[string]any)["id"].(string)
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO comments(id,thread_id,author_id,body) SELECT 'bulk_'||g,$1,$2,'x' FROM generate_series(2,$3) g`, threadID, f.viewer.ID, store.MaxCommentsPerArtifact); err != nil {
		t.Fatal(err)
	}
	code, out = f.do(&f.viewer, http.MethodPost, "/api/v1/threads/"+threadID+"/comments", map[string]any{"body": "over"})
	expectStatus(t, "reply over limit", code, http.StatusConflict, out)
}
