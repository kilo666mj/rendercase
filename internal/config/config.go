package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr          string
	PublicURL           *url.URL
	ContentURL          *url.URL
	DatabaseURL         string
	StorageRoot         string
	CookieSecret        []byte
	OIDCIssuer          string
	OIDCClientID        string
	OIDCClientSecret    string
	OIDCRedirectURL     string
	AdminSubjects       map[string]struct{}
	OAuthAudience       string
	OAuthAudiences      []string
	OAuthScope          string
	MaxBundleBytes      int64
	MaxFiles            int
	UploadTTL           time.Duration
	ViewerTicketTTL     time.Duration
	SessionTTL          time.Duration
	MaintenanceInterval time.Duration
	AuditRetention      time.Duration
	TrustProxyCIDRs     []netip.Prefix
}

func Load() (Config, error) {
	publicURL, err := requiredURL("RENDERCASE_PUBLIC_URL")
	if err != nil {
		return Config{}, err
	}
	contentURL, err := requiredURL("RENDERCASE_CONTENT_URL")
	if err != nil {
		return Config{}, err
	}
	if publicURL.Scheme != "https" && !isLoopbackURL(publicURL) {
		return Config{}, errors.New("RENDERCASE_PUBLIC_URL must use https outside localhost")
	}
	if contentURL.Scheme != "https" && !isLoopbackURL(contentURL) {
		return Config{}, errors.New("RENDERCASE_CONTENT_URL must use https outside localhost")
	}
	if strings.EqualFold(publicURL.Hostname(), contentURL.Hostname()) {
		return Config{}, errors.New("content and management URLs must use separate hostnames")
	}

	secretText := strings.TrimSpace(os.Getenv("RENDERCASE_COOKIE_SECRET"))
	secret, err := base64.StdEncoding.DecodeString(secretText)
	if err != nil || len(secret) < 32 {
		return Config{}, errors.New("RENDERCASE_COOKIE_SECRET must be base64 for at least 32 bytes")
	}

	trustedProxies, err := prefixes(os.Getenv("RENDERCASE_TRUSTED_PROXIES"))
	if err != nil {
		return Config{}, fmt.Errorf("RENDERCASE_TRUSTED_PROXIES: %w", err)
	}
	cfg := Config{
		ListenAddr:          envDefault("RENDERCASE_LISTEN", "127.0.0.1:18100"),
		PublicURL:           publicURL,
		ContentURL:          contentURL,
		DatabaseURL:         strings.TrimSpace(os.Getenv("RENDERCASE_DATABASE_URL")),
		StorageRoot:         envDefault("RENDERCASE_STORAGE_ROOT", "/var/lib/rendercase/artifacts"),
		CookieSecret:        secret,
		OIDCIssuer:          strings.TrimRight(strings.TrimSpace(os.Getenv("RENDERCASE_OIDC_ISSUER")), "/"),
		OIDCClientID:        strings.TrimSpace(os.Getenv("RENDERCASE_OIDC_CLIENT_ID")),
		OIDCClientSecret:    strings.TrimSpace(os.Getenv("RENDERCASE_OIDC_CLIENT_SECRET")),
		OIDCRedirectURL:     strings.TrimSpace(os.Getenv("RENDERCASE_OIDC_REDIRECT_URL")),
		AdminSubjects:       csvSet(os.Getenv("RENDERCASE_ADMIN_SUBJECTS")),
		OAuthAudience:       envDefault("RENDERCASE_OAUTH_AUDIENCE", publicURL.String()+"mcp"),
		OAuthScope:          envDefault("RENDERCASE_OAUTH_SCOPE", "rendercase:mcp"),
		MaxBundleBytes:      envInt64("RENDERCASE_MAX_BUNDLE_BYTES", 25<<20),
		MaxFiles:            envInt("RENDERCASE_MAX_FILES", 500),
		UploadTTL:           envDuration("RENDERCASE_UPLOAD_TTL", 15*time.Minute),
		ViewerTicketTTL:     envDuration("RENDERCASE_VIEWER_TICKET_TTL", 5*time.Minute),
		SessionTTL:          envDuration("RENDERCASE_SESSION_TTL", 12*time.Hour),
		MaintenanceInterval: envDuration("RENDERCASE_MAINTENANCE_INTERVAL", time.Hour),
		AuditRetention:      envDuration("RENDERCASE_AUDIT_RETENTION", 365*24*time.Hour),
		TrustProxyCIDRs:     trustedProxies,
	}
	cfg.OAuthAudiences = csv(os.Getenv("RENDERCASE_OAUTH_AUDIENCES"))
	if len(cfg.OAuthAudiences) == 0 {
		cfg.OAuthAudiences = []string{cfg.OAuthAudience}
	} else if !contains(cfg.OAuthAudiences, cfg.OAuthAudience) {
		cfg.OAuthAudiences = append([]string{cfg.OAuthAudience}, cfg.OAuthAudiences...)
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("RENDERCASE_DATABASE_URL is required")
	}
	if cfg.OIDCIssuer == "" || cfg.OIDCClientID == "" || cfg.OIDCRedirectURL == "" {
		return Config{}, errors.New("OIDC issuer, client ID, and redirect URL are required")
	}
	if cfg.MaxBundleBytes <= 0 || cfg.MaxFiles <= 0 || cfg.MaintenanceInterval <= 0 || cfg.AuditRetention <= 0 {
		return Config{}, errors.New("bundle and maintenance limits must be positive")
	}
	return cfg, nil
}

func prefixes(value string) ([]netip.Prefix, error) {
	values := csv(value)
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			addr, addrErr := netip.ParseAddr(value)
			if addrErr != nil {
				return nil, fmt.Errorf("invalid IP or CIDR %q", value)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		out = append(out, prefix.Masked())
	}
	return out, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func requiredURL(name string) (*url.URL, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("%s must be an absolute URL", name)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u, nil
}

func isLoopbackURL(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func envDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func envInt64(name string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return d
}

func csv(raw string) []string {
	var values []string
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func csvSet(raw string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, value := range csv(raw) {
		set[value] = struct{}{}
	}
	return set
}
