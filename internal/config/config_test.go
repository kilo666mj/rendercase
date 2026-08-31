package config

import (
	"encoding/base64"
	"net/netip"
	"strings"
	"testing"
)

func TestPrefixesAcceptsAddressesAndCIDRs(t *testing.T) {
	got, err := prefixes("192.0.2.10, 2001:db8::/64")
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32"), netip.MustParsePrefix("2001:db8::/64")}
	if len(got) != len(want) {
		t.Fatalf("got %d prefixes, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("prefix %d = %v, want %v", i, got[i], want[i])
		}
	}
	if _, err := prefixes("not-an-address"); err == nil {
		t.Fatal("invalid trusted proxy accepted")
	}
}

func TestLoadAuthModes(t *testing.T) {
	setRequiredEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthMode != AuthModeOIDC {
		t.Fatalf("default auth mode = %q", cfg.AuthMode)
	}

	t.Setenv("RENDERCASE_AUTH_MODE", AuthModeCloudflareAccess)
	t.Setenv("RENDERCASE_OIDC_CLIENT_ID", "")
	t.Setenv("RENDERCASE_OIDC_REDIRECT_URL", "")
	t.Setenv("RENDERCASE_CF_ACCESS_TEAM_DOMAIN", "https://example.cloudflareaccess.com/")
	t.Setenv("RENDERCASE_CF_ACCESS_AUD", "access-audience")
	t.Setenv("RENDERCASE_ADMIN_GROUPS", "rendercase-admins, platform-admins")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CFAccessTeamDomain != "https://example.cloudflareaccess.com" {
		t.Fatalf("team domain = %q", cfg.CFAccessTeamDomain)
	}
	if _, ok := cfg.AdminGroups["rendercase-admins"]; !ok {
		t.Fatal("admin groups were not loaded")
	}
}

func TestLoadRejectsInvalidCloudflareAccessConfig(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("RENDERCASE_AUTH_MODE", AuthModeCloudflareAccess)
	t.Setenv("RENDERCASE_CF_ACCESS_AUD", "access-audience")
	for _, domain := range []string{"", "http://example.cloudflareaccess.com", "https://example.cloudflareaccess.com/path", "https://user@example.cloudflareaccess.com"} {
		t.Run(domain, func(t *testing.T) {
			t.Setenv("RENDERCASE_CF_ACCESS_TEAM_DOMAIN", domain)
			if _, err := Load(); err == nil {
				t.Fatalf("invalid team domain %q accepted", domain)
			}
		})
	}
}

func TestLoadS3Storage(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("RENDERCASE_STORAGE_BACKEND", StorageBackendS3)
	t.Setenv("RENDERCASE_STORAGE_ROOT", "/tmp/rendercase-stage")
	t.Setenv("RENDERCASE_S3_BUCKET", "artifacts")
	t.Setenv("RENDERCASE_S3_PREFIX", "/rendercase/production/")
	t.Setenv("RENDERCASE_S3_REGION", "eu-central-1")
	t.Setenv("RENDERCASE_S3_ENDPOINT", "https://objects.example.com")
	t.Setenv("RENDERCASE_S3_USE_PATH_STYLE", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorageBackend != StorageBackendS3 || cfg.S3Bucket != "artifacts" || cfg.S3Prefix != "rendercase/production" || !cfg.S3UsePathStyle {
		t.Fatalf("S3 config = %+v", cfg)
	}
}

func TestLoadRejectsInvalidS3Storage(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("RENDERCASE_STORAGE_BACKEND", StorageBackendS3)
	if _, err := Load(); err == nil {
		t.Fatal("S3 storage without a bucket was accepted")
	}
	t.Setenv("RENDERCASE_S3_BUCKET", "artifacts")
	for _, endpoint := range []string{"http://objects.example.com", "https://user@objects.example.com", "https://objects.example.com?query=yes"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Setenv("RENDERCASE_S3_ENDPOINT", endpoint)
			if _, err := Load(); err == nil {
				t.Fatalf("invalid S3 config with endpoint %q accepted", endpoint)
			}
		})
	}
}

func TestLoadRejectsInvalidS3PathStyle(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("RENDERCASE_S3_USE_PATH_STYLE", "sometimes")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "RENDERCASE_S3_USE_PATH_STYLE") {
		t.Fatalf("Load() error = %v", err)
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RENDERCASE_PUBLIC_URL", "https://rendercase.example.com")
	t.Setenv("RENDERCASE_CONTENT_URL", "https://content.rendercase.example.com")
	t.Setenv("RENDERCASE_COOKIE_SECRET", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("RENDERCASE_DATABASE_URL", "postgres://rendercase@example/rendercase")
	t.Setenv("RENDERCASE_OIDC_ISSUER", "https://id.example.com")
	t.Setenv("RENDERCASE_OIDC_CLIENT_ID", "rendercase")
	t.Setenv("RENDERCASE_OIDC_REDIRECT_URL", "https://rendercase.example.com/api/v1/auth/oidc/callback")
}
