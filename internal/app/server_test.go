package app

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/kilo666mj/rendercase/internal/config"
	"github.com/kilo666mj/rendercase/internal/store"
)

func TestClientAddressTrustsHeadersOnlyFromConfiguredProxy(t *testing.T) {
	s := &Server{cfg: config.Config{TrustProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32")}}}

	trusted := httptest.NewRequest("GET", "https://rendercase.example/", nil)
	trusted.RemoteAddr = "192.0.2.10:1234"
	trusted.Header.Set("CF-Connecting-IP", "203.0.113.8")
	if got := s.clientAddress(trusted); got != netip.MustParseAddr("203.0.113.8") {
		t.Fatalf("trusted client address = %v", got)
	}

	untrusted := httptest.NewRequest("GET", "https://rendercase.example/", nil)
	untrusted.RemoteAddr = "192.0.2.99:1234"
	untrusted.Header.Set("CF-Connecting-IP", "203.0.113.8")
	if got := s.clientAddress(untrusted); got != netip.MustParseAddr("192.0.2.99") {
		t.Fatalf("untrusted client address = %v", got)
	}
}

func TestViewerShowsSharingControlsOnlyToOwner(t *testing.T) {
	tpl, err := template.ParseFS(webFS, "web/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"User":          &store.User{DisplayName: "Owner"},
		"Version":       store.Version{ArtifactID: "a_example", Version: 3, Title: "Example"},
		"ContentSource": "https://content.example/t/ticket/a_example/3/index.html",
		"CanShare":      true,
	}
	var rendered bytes.Buffer
	if err := tpl.ExecuteTemplate(&rendered, "viewer", data); err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{`class="mark-signal"`, `id="share-open"`, `id="share-dialog"`, `data-artifact="a_example"`, `Manage sharing`, `Create a new link`, `Create link for version 3`} {
		if !strings.Contains(rendered.String(), wanted) {
			t.Errorf("owner viewer missing %q", wanted)
		}
	}

	data["CanShare"] = false
	rendered.Reset()
	if err := tpl.ExecuteTemplate(&rendered, "viewer", data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered.String(), `id="share-open"`) || strings.Contains(rendered.String(), `id="share-dialog"`) {
		t.Fatal("sharing controls rendered for a non-owner")
	}
}

func TestUploadTokenIgnoresQueryString(t *testing.T) {
	request := httptest.NewRequest(http.MethodPut, "https://rendercase.example/upload?token=logged-secret", nil)
	if got := uploadToken(request); got != "" {
		t.Fatalf("query-string upload token accepted: %q", got)
	}
	request.Header.Set("X-Rendercase-Upload-Token", "header-secret")
	if got := uploadToken(request); got != "header-secret" {
		t.Fatalf("header upload token = %q", got)
	}
}

func TestSafeReturnRejectsExternalURLForms(t *testing.T) {
	for _, value := range []string{"", "https://evil.example", "//evil.example", "/\\evil.example", "/path\r\nLocation: https://evil.example"} {
		if got := safeReturn(value); got != "/" {
			t.Errorf("safeReturn(%q) = %q", value, got)
		}
	}
	if got := safeReturn("/a/artifact?version=2"); got != "/a/artifact?version=2" {
		t.Fatalf("safe internal return changed to %q", got)
	}
}

func TestNormalizeTitle(t *testing.T) {
	if got, err := normalizeTitle("  Example  "); err != nil || got != "Example" {
		t.Fatalf("normalizeTitle = %q, %v", got, err)
	}
	if _, err := normalizeTitle("   "); err == nil {
		t.Fatal("blank title accepted")
	}
	if _, err := normalizeTitle(strings.Repeat("x", maxTitleBytes+1)); err == nil {
		t.Fatal("oversized title accepted")
	}
}

func TestValidateShareOptions(t *testing.T) {
	now := time.Now()
	validVersion, validLimit, invalidLimit := 2, 3, -1
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	if err := validateShareOptions(&validVersion, &future, &validLimit, 2, now); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	for _, test := range []struct {
		version *int
		expires *time.Time
		limit   *int
	}{
		{nil, &future, &validLimit},
		{&validVersion, &past, &validLimit},
		{&validVersion, &future, &invalidLimit},
	} {
		if err := validateShareOptions(test.version, test.expires, test.limit, 2, now); err == nil {
			t.Fatalf("invalid options accepted: %+v", test)
		}
	}
}
