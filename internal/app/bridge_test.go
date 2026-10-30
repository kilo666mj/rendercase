package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kilo666mj/rendercase/internal/config"
	"github.com/kilo666mj/rendercase/internal/securetoken"
)

func TestBridgeScriptServedOnlyOnContentHost(t *testing.T) {
	s := &Server{cfg: config.Config{
		PublicURL:  mustParseURL(t, "https://rendercase.example/"),
		ContentURL: mustParseURL(t, "https://content.rendercase.example/"),
	}, mcp: http.NotFoundHandler()}
	want, err := webFS.ReadFile("web/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://content.rendercase.example/_rc/bridge.js", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || rec.Body.String() != string(want) {
		t.Fatalf("content host bridge: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://rendercase.example/_rc/bridge.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("management host bridge: %d", rec.Code)
	}
}

func TestBridgeScriptTrustBoundaries(t *testing.T) {
	source, err := webFS.ReadFile("web/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"e.source!==window.parent",
		"parentOrigin=e.origin",
		"postMessage(m,parentOrigin)",
		"attachShadow({mode:'closed'})",
		"textContent=String(n)",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("bridge.js missing %q", required)
		}
	}
	for _, forbidden := range []string{"innerHTML", "insertAdjacentHTML", "document.write", "eval(", "fetch("} {
		if strings.Contains(text, forbidden) {
			t.Errorf("bridge.js contains %q", forbidden)
		}
	}
}

func TestTicketSubjects(t *testing.T) {
	if got := contentTicketSubject("a_x", 3, false); got != "a_x:3" {
		t.Fatalf("plain subject = %q", got)
	}
	if got := contentTicketSubject("a_x", 3, true); got != "a_x:3:c" {
		t.Fatalf("comment subject = %q", got)
	}
	for _, tc := range []struct {
		subject      string
		ok, comments bool
	}{
		{"a_x:3", true, false},
		{"a_x:3:c", true, true},
		{"a_x:4", false, false},
		{"a_x:3:d", false, false},
		{"a_y:3:c", false, false},
	} {
		ok, comments := ticketAllows(tc.subject, "a_x", 3)
		if ok != tc.ok || comments != tc.comments {
			t.Errorf("%q: ok=%v comments=%v", tc.subject, ok, comments)
		}
	}
}

func TestInjectBridgePlacement(t *testing.T) {
	tag := string(bridgeTag)
	for _, tc := range []struct{ name, in, want string }{
		{"after head", "<!doctype html><html><head><title>x</title></head></html>", "<!doctype html><html><head>" + tag + "<title>x</title></head></html>"},
		{"head with attributes", `<html><head lang="en"><title>x</title>`, `<html><head lang="en">` + tag + `<title>x</title>`},
		{"uppercase head", "<HTML><HEAD><TITLE>x</TITLE>", "<HTML><HEAD>" + tag + "<TITLE>x</TITLE>"},
		{"skips header", "<html><body><header>h</header>", "<html>" + tag + "<body><header>h</header>"},
		{"html only", "<html lang=en><p>x", "<html lang=en>" + tag + "<p>x"},
		{"doctype only", "<!DOCTYPE html><p>x", "<!DOCTYPE html>" + tag + "<p>x"},
		{"fragment", "<p>x</p>", tag + "<p>x</p>"},
		{"bom fragment", "\uFEFF<p>x</p>", "\uFEFF" + tag + "<p>x</p>"},
		{"multibyte before head", "<!-- İİİ ünïcode --><head><title>x", "<!-- İİİ ünïcode --><head>" + tag + "<title>x"},
		{"unterminated head", "<html><head", "<html>" + tag + "<head"},
		{"head inside comment", "<!-- <head> --><html><head><title>x", "<!-- <head> --><html><head>" + tag + "<title>x"},
		{"only commented head", "<!-- <head> --><html><p>x", "<!-- <head> --><html>" + tag + "<p>x"},
		{"only commented tags", "<!doctype html><!-- <head><html> --><p>x", "<!doctype html>" + tag + "<!-- <head><html> --><p>x"},
		{"commented fragment", "<!-- <head> --><p>x", tag + "<!-- <head> --><p>x"},
		{"unterminated comment", "<html><!-- <head><p>x", "<html>" + tag + "<!-- <head><p>x"},
		{"head attribute with gt", "<!-- a --><head data-x=\"1\"><title>x", "<!-- a --><head data-x=\"1\">" + tag + "<title>x"},
	} {
		if got := string(injectBridge([]byte(tc.in))); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
	late := strings.Repeat(" ", bridgeScanBytes) + "<head>"
	if got := string(injectBridge([]byte(late))); !strings.HasPrefix(got, tag) {
		t.Errorf("head beyond scan window was matched")
	}
}

func TestContentInjectsBridgeOnlyForCommentTicketsIntegration(t *testing.T) {
	s, db := testCommentServer(t)
	ctx := t.Context()
	owner, err := db.UpsertUser(ctx, "owner-subject", "owner", "owner@example.com", "Owner", false)
	if err != nil {
		t.Fatal(err)
	}
	index := "<!doctype html><html><head><title>x</title></head><body><p>Hello</p></body></html>"
	big := "<!doctype html><head></head>" + strings.Repeat("a", maxBridgeInjectBytes)
	script := "console.log('hi')"
	out, err := s.publishBundleForUser(ctx, owner, publishInput{Title: "Injected", BundleBase64: zipBundle(t, map[string]string{"index.html": index, "big.html": big, "app.js": script})})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Artifact.ID
	get := func(subject, file string, header map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		ticket := securetoken.Sign(s.cfg.CookieSecret, subject, time.Now().Add(time.Minute))
		req := httptest.NewRequest(http.MethodGet, "https://content.rendercase.example/t/"+ticket+"/"+id+"/1/"+file, nil)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	commentSubject, shareSubject := contentTicketSubject(id, 1, true), contentTicketSubject(id, 1, false)

	rec := get(commentSubject, "index.html", nil)
	wantInjected := strings.Replace(index, "<head>", "<head>"+string(bridgeTag), 1)
	if rec.Code != http.StatusOK || rec.Body.String() != wantInjected || rec.Header().Get("Content-Length") != strconv.Itoa(len(wantInjected)) {
		t.Fatalf("comment ticket index: %d %q len=%s", rec.Code, rec.Body.String(), rec.Header().Get("Content-Length"))
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "connect-src 'none'") {
		t.Fatalf("injected response lost the content CSP")
	}
	rec = get(commentSubject, "index.html", map[string]string{"Range": "bytes=0-4"})
	if rec.Code != http.StatusOK || rec.Body.String() != wantInjected {
		t.Fatalf("comment ticket range: %d", rec.Code)
	}
	rec = get(shareSubject, "index.html", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != index {
		t.Fatalf("share ticket index not byte-identical: %q", rec.Body.String())
	}
	rec = get(shareSubject, "index.html", map[string]string{"Range": "bytes=0-4"})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != index[:5] {
		t.Fatalf("share ticket range: %d %q", rec.Code, rec.Body.String())
	}
	rec = get(commentSubject, "big.html", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), []byte(big)) {
		t.Fatalf("oversized HTML was modified: %d len=%d", rec.Code, rec.Body.Len())
	}
	rec = get(commentSubject, "app.js", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != script {
		t.Fatalf("script was modified: %q", rec.Body.String())
	}
	rec = get(contentTicketSubject(id, 2, true), "index.html", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ticket for another version: %d", rec.Code)
	}
}
