package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/kilo666mj/rendercase/internal/config"
)

type rsaKeySet struct{ publicKey *rsa.PublicKey }

func (k rsaKeySet) VerifySignature(_ context.Context, raw string) ([]byte, error) {
	signed, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return nil, err
	}
	return signed.Verify(k.publicKey)
}

func TestVerifyCloudflareAccess(t *testing.T) {
	const issuer = "https://example.cloudflareaccess.com"
	const audience = "access-audience"
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	verifier := oidc.NewVerifier(issuer, rsaKeySet{publicKey: &key.PublicKey}, &oidc.Config{ClientID: audience})
	s := &Server{cfVerifier: verifier}

	valid := signAccessJWT(t, signer, issuer, audience, map[string]any{
		"sub": "identity-subject", "email": "person@example.com", "name": "Example Person", "type": "app", "groups": []string{"developers"}, "custom": map[string]any{"groups": []string{"developers", "rendercase-admins"}},
	})
	request := httptest.NewRequest(http.MethodGet, "https://rendercase.example.com/", nil)
	request.Header.Set(cfAccessJWTHeader, valid)
	identity, err := s.verifyCloudflareAccess(request)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "identity-subject" || identity.Email != "person@example.com" || identity.Name != "Example Person" {
		t.Fatalf("identity = %+v", identity)
	}
	if len(identity.Groups) != 2 || identity.Groups[1] != "rendercase-admins" {
		t.Fatalf("groups = %#v", identity.Groups)
	}

	request.Header.Set(cfAccessJWTHeader, signAccessJWT(t, signer, issuer, "wrong-audience", map[string]any{"sub": "subject", "email": "person@example.com", "type": "app"}))
	if _, err := s.verifyCloudflareAccess(request); err == nil {
		t.Fatal("wrong audience accepted")
	}
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: otherKey}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(cfAccessJWTHeader, signAccessJWT(t, otherSigner, issuer, audience, map[string]any{"sub": "subject", "email": "person@example.com", "type": "app"}))
	if _, err := s.verifyCloudflareAccess(request); err == nil {
		t.Fatal("untrusted signature accepted")
	}
	request.Header.Del(cfAccessJWTHeader)
	if _, err := s.verifyCloudflareAccess(request); err == nil {
		t.Fatal("missing assertion accepted")
	}
}

func TestVerifyCloudflareAccessBearerUsesSameJWTContract(t *testing.T) {
	const issuer = "https://example.cloudflareaccess.com"
	const audience = "access-audience"
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfVerifier: oidc.NewVerifier(issuer, rsaKeySet{publicKey: &key.PublicKey}, &oidc.Config{ClientID: audience})}
	raw := signAccessJWT(t, signer, issuer, audience, map[string]any{"sub": "agent-subject", "email": "agent@example.com", "type": "app", "custom": map[string]any{"groups": []string{"rendercase-admins"}}})
	identity, err := s.verifyCloudflareAccessJWT(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "agent-subject" || identity.Email != "agent@example.com" || len(identity.Groups) != 1 || identity.Groups[0] != "rendercase-admins" {
		t.Fatalf("identity = %+v", identity)
	}
	if _, err := s.verifyCloudflareAccessJWT(context.Background(), signAccessJWT(t, signer, issuer, "wrong-audience", map[string]any{"sub": "agent-subject", "email": "agent@example.com", "type": "app"})); err == nil {
		t.Fatal("wrong bearer audience accepted")
	}
	if _, err := s.verifyCloudflareAccessJWT(context.Background(), signAccessJWT(t, signer, issuer, audience, map[string]any{"sub": "agent-subject", "email": "agent@example.com", "type": "org"})); err == nil {
		t.Fatal("non-application bearer token accepted")
	}
}

func TestCloudflareAccessBrowserRoutes(t *testing.T) {
	publicURL, err := url.Parse("https://rendercase.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: config.Config{AuthMode: config.AuthModeCloudflareAccess, PublicURL: publicURL}}

	response := httptest.NewRecorder()
	s.login(response, httptest.NewRequest(http.MethodGet, "https://rendercase.example.com/auth/login", nil))
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/" {
		t.Fatalf("login response = %d, location %q", response.Code, response.Header().Get("Location"))
	}

	response = httptest.NewRecorder()
	s.callback(response, httptest.NewRequest(http.MethodGet, "https://rendercase.example.com/api/v1/auth/oidc/callback", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("callback status = %d", response.Code)
	}

	response = httptest.NewRecorder()
	s.logout(response, httptest.NewRequest(http.MethodPost, "https://rendercase.example.com/auth/logout", nil))
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "https://rendercase.example.com/cdn-cgi/access/logout" {
		t.Fatalf("logout response = %d, location %q", response.Code, response.Header().Get("Location"))
	}

	response = httptest.NewRecorder()
	s.requireUser(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unauthenticated request reached handler")
	})).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://rendercase.example.com/", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.Code)
	}
}

func TestCloudflareAccessAdminMapping(t *testing.T) {
	s := &Server{cfg: config.Config{
		AdminSubjects: map[string]struct{}{"admin-subject": {}},
		AdminGroups:   map[string]struct{}{"rendercase-admins": {}},
	}}
	if !s.cloudflareAccessAdmin(cloudflareAccessIdentity{Subject: "admin-subject"}) {
		t.Fatal("configured admin subject was not granted access")
	}
	if !s.cloudflareAccessAdmin(cloudflareAccessIdentity{Subject: "user", Groups: []string{"rendercase-admins"}}) {
		t.Fatal("configured admin group was not granted access")
	}
	if s.cloudflareAccessAdmin(cloudflareAccessIdentity{Subject: "user", Groups: []string{"developers"}}) {
		t.Fatal("unconfigured identity was granted admin access")
	}
}

func TestVerifyCloudflareAccessRequiresStableIdentityClaims(t *testing.T) {
	const issuer = "https://example.cloudflareaccess.com"
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfVerifier: oidc.NewVerifier(issuer, rsaKeySet{publicKey: &key.PublicKey}, &oidc.Config{ClientID: "aud"})}
	for _, claims := range []map[string]any{{"email": "person@example.com", "type": "app"}, {"sub": "subject", "type": "app"}} {
		request := httptest.NewRequest(http.MethodGet, "https://rendercase.example.com/", nil)
		request.Header.Set(cfAccessJWTHeader, signAccessJWT(t, signer, issuer, "aud", claims))
		if _, err := s.verifyCloudflareAccess(request); err == nil {
			t.Fatalf("incomplete claims accepted: %#v", claims)
		}
	}
}

func signAccessJWT(t *testing.T, signer jose.Signer, issuer, audience string, privateClaims map[string]any) string {
	t.Helper()
	now := time.Now()
	standard := jwt.Claims{
		Issuer: issuer, Audience: jwt.Audience{audience}, IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), Expiry: jwt.NewNumericDate(now.Add(time.Hour)),
	}
	raw, err := jwt.Signed(signer).Claims(standard).Claims(privateClaims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
