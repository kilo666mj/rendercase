package app

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
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
		"Artifact":      store.Artifact{Visibility: "authenticated"},
		"ContentSource": "https://content.example/t/ticket/a_example/3/index.html",
		"CanShare":      true,
	}
	var rendered bytes.Buffer
	if err := tpl.ExecuteTemplate(&rendered, "viewer", data); err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{`stroke="currentColor"`, `fill="var(--accent)"`, `id="share-open"`, `id="share-dialog"`, `data-artifact="a_example"`, `Manage sharing`, `Who can open this artifact?`, `Private — only you`, `All accounts — anyone signed in`, `value="authenticated" selected`, `PUBLIC LINKS · NO ACCOUNT REQUIRED`, `async function responseData(response)`, `Create a public link`, `Create public link for version 3`} {
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

func TestTemplatesIncludeFavicon(t *testing.T) {
	tpl, err := template.ParseFS(webFS, "web/*.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		data map[string]any
	}{
		{name: "index", data: map[string]any{"User": &store.User{}}},
		{name: "login", data: map[string]any{}},
		{name: "viewer", data: map[string]any{"Version": store.Version{}}},
		{name: "admin", data: map[string]any{"User": &store.User{Admin: true}}},
	} {
		var rendered bytes.Buffer
		if err := tpl.ExecuteTemplate(&rendered, test.name, test.data); err != nil {
			t.Fatalf("render %s: %v", test.name, err)
		}
		if !strings.Contains(rendered.String(), `<link rel="icon" href="/static/favicon.svg"`) {
			t.Errorf("%s template does not include the favicon", test.name)
		}
	}
}

func TestValidateBranding(t *testing.T) {
	valid := store.Branding{ThemeName: "Acme dark", SiteName: "Acme", BackgroundColor: "#101010", PanelColor: "#202020", TextColor: "#ffffff", MutedColor: "#aaaaaa", PrimaryColor: "#00ffaa", AccentColor: "#ff00aa"}
	if err := validateBranding(valid); err != nil {
		t.Fatalf("valid branding: %v", err)
	}
	valid.PrimaryColor = "red; background:url(https://example.invalid)"
	if err := validateBranding(valid); err == nil {
		t.Fatal("unsafe CSS color accepted")
	}
}

func TestRequireAdminRejectsOrdinaryUsers(t *testing.T) {
	s := &Server{}
	handler := s.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	request := httptest.NewRequest(http.MethodGet, "https://rendercase.example/admin", nil)
	request = request.WithContext(context.WithValue(request.Context(), userContextKey{}, store.User{ID: "ordinary"}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("ordinary user status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "https://rendercase.example/admin", nil)
	request = request.WithContext(context.WithValue(request.Context(), userContextKey{}, store.User{ID: "admin", Admin: true}))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("administrator status = %d", response.Code)
	}
}

func TestAdminTemplateContainsOnlyAdministrativeRemovalControls(t *testing.T) {
	tpl, err := template.ParseFS(webFS, "web/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"User": &store.User{Admin: true}, "Artifacts": []store.AdminArtifact{{
		Artifact: store.Artifact{ID: "a_example", Title: "Example", LatestVersion: 2}, OwnerEmail: "owner@example.com",
		Shares: []store.Share{{ID: "s_example", ArtifactID: "a_example"}},
	}}}
	var rendered bytes.Buffer
	if err := tpl.ExecuteTemplate(&rendered, "admin", data); err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{`data-delete-artifact="a_example"`, `data-revoke-share="s_example"`, "owner@example.com", "Administrative actions are audited"} {
		if !strings.Contains(rendered.String(), wanted) {
			t.Errorf("admin template missing %q", wanted)
		}
	}
	for _, forbidden := range []string{"Publish version", "Edit artifact"} {
		if strings.Contains(rendered.String(), forbidden) {
			t.Errorf("admin template unexpectedly grants %q", forbidden)
		}
	}
}

func TestFavicon(t *testing.T) {
	response := httptest.NewRecorder()
	(&Server{}).favicon(response, httptest.NewRequest(http.MethodGet, "/static/favicon.svg", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	for _, wanted := range []string{`stroke="#67e8f9"`, `fill="#2dd4bf"`, `fill="#a78bfa"`} {
		if !strings.Contains(response.Body.String(), wanted) {
			t.Errorf("favicon missing %q", wanted)
		}
	}
}

func TestPrimaryButtonUsesBrandingTokens(t *testing.T) {
	css, err := webFS.ReadFile("web/style.css")
	if err != nil {
		t.Fatal(err)
	}
	style := string(css)
	for _, wanted := range []string{".button{background:var(--accent)", "color:var(--panel)", ".button:hover{background:var(--violet)"} {
		if !strings.Contains(style, wanted) {
			t.Errorf("button style missing %q", wanted)
		}
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

func TestUploadURLUsesDedicatedCapabilityPath(t *testing.T) {
	s := &Server{cfg: config.Config{PublicURL: mustParseURL(t, "https://rendercase.example/base/")}}
	if got, want := s.uploadURL("u_example"), "https://rendercase.example/base/upload/u_example"; got != want {
		t.Fatalf("upload URL = %q, want %q", got, want)
	}
}

func TestUploadCapabilityPathAcceptsOnlyPUT(t *testing.T) {
	s := &Server{cfg: config.Config{
		PublicURL:  mustParseURL(t, "https://rendercase.example/"),
		ContentURL: mustParseURL(t, "https://content.rendercase.example/"),
	}, mcp: http.NotFoundHandler()}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(method, "https://rendercase.example/upload/u_example", nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /upload status = %d", method, response.Code)
		}
	}
}

func TestViewerRoutesSeparateUserAndShareAuthentication(t *testing.T) {
	s := &Server{cfg: config.Config{
		AuthMode:   config.AuthModeOIDC,
		PublicURL:  mustParseURL(t, "https://rendercase.example/"),
		ContentURL: mustParseURL(t, "https://content.rendercase.example/"),
	}, mcp: http.NotFoundHandler()}

	response := httptest.NewRecorder()
	privateRequest := httptest.NewRequest(http.MethodGet, "https://rendercase.example/a/a_example", nil)
	privateRequest.AddCookie(&http.Cookie{Name: shareCookieName, Value: "share-session"})
	s.Handler().ServeHTTP(response, privateRequest)
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/auth/login?return=%2Fa%2Fa_example" {
		t.Fatalf("private viewer response = %d, location %q", response.Code, response.Header().Get("Location"))
	}

	response = httptest.NewRecorder()
	sharedRequest := httptest.NewRequest(http.MethodGet, "https://rendercase.example/shared/a_example", nil)
	sharedRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "user-session"})
	s.Handler().ServeHTTP(response, sharedRequest)
	if response.Code != http.StatusNotFound || response.Header().Get("Location") != "" {
		t.Fatalf("shared viewer response = %d, location %q", response.Code, response.Header().Get("Location"))
	}

	if got, want := sharedArtifactPath("a_example"), "/shared/a_example"; got != want {
		t.Fatalf("shared artifact path = %q, want %q", got, want)
	}
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestSecurityCookiesUseHostPrefix(t *testing.T) {
	for _, name := range []string{sessionCookieName, shareCookieName} {
		response := httptest.NewRecorder()
		setCookie(response, name, "secret", time.Now().Add(time.Hour), true)
		cookies := response.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("%s: got %d cookies", name, len(cookies))
		}
		cookie := cookies[0]
		if !strings.HasPrefix(cookie.Name, "__Host-") || cookie.Path != "/" || cookie.Domain != "" || !cookie.Secure {
			t.Fatalf("insecure cookie attributes: %+v", cookie)
		}
	}
}

func TestShareListBuildsDynamicContentWithDOMAPIs(t *testing.T) {
	source, err := webFS.ReadFile("web/viewer.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, forbidden := range []string{"function esc(", "list.innerHTML", `data-revoke="'+`} {
		if strings.Contains(text, forbidden) {
			t.Errorf("share list retains unsafe HTML construction %q", forbidden)
		}
	}
	for _, required := range []string{"id.textContent=String(s.ID)", "description.textContent=summary(s)", "button.dataset.revoke=String(s.ID)"} {
		if !strings.Contains(text, required) {
			t.Errorf("share list missing DOM assignment %q", required)
		}
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
