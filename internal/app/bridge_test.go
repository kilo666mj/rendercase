package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kilo666mj/rendercase/internal/config"
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
