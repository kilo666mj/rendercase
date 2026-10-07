package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/kilo666mj/rendercase/internal/config"
	"github.com/kilo666mj/rendercase/internal/store"
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

func TestRequireBearerCloudflareAccessAcceptsAssertionWithoutAuthorizationHeader(t *testing.T) {
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
	publicURL, err := url.Parse("https://rendercase.example.com")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg:        config.Config{AuthMode: config.AuthModeCloudflareAccess, PublicURL: publicURL},
		cfVerifier: oidc.NewVerifier(issuer, rsaKeySet{publicKey: &key.PublicKey}, &oidc.Config{ClientID: audience}),
		cloudflareUserUpsert: func(_ context.Context, user store.User) (store.User, error) {
			user.ID = "cloudflare-user"
			return user, nil
		},
	}
	request := httptest.NewRequest(http.MethodPost, publicURL.JoinPath("mcp").String(), nil)
	request.Header.Set(cfAccessJWTHeader, signAccessJWT(t, signer, issuer, audience, map[string]any{
		"sub": "agent-subject", "email": "agent@example.com", "type": "app",
	}))
	response := httptest.NewRecorder()
	called := false

	s.requireBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if user := currentUser(r); user.ID != "cloudflare-user" || user.Subject != "cloudflare_access:agent-subject" {
			t.Fatalf("current user = %+v", user)
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, request)
	if !called || response.Code != http.StatusNoContent {
		t.Fatalf("called = %v, status = %d", called, response.Code)
	}
}

func TestRequireBearerCloudflareAccessRejectsMissingOrInvalidAssertion(t *testing.T) {
	publicURL, err := url.Parse("https://rendercase.example.com")
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg:        config.Config{AuthMode: config.AuthModeCloudflareAccess, PublicURL: publicURL},
		cfVerifier: oidc.NewVerifier("https://example.cloudflareaccess.com", rsaKeySet{publicKey: &key.PublicKey}, &oidc.Config{ClientID: "access-audience"}),
	}
	for _, test := range []struct{ name, assertion string }{{name: "missing"}, {name: "invalid", assertion: "not-a-jwt"}} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, publicURL.JoinPath("mcp").String(), nil)
			request.Header.Set(cfAccessJWTHeader, test.assertion)
			response := httptest.NewRecorder()
			called := false
			s.requireBearer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(response, request)
			if called || response.Code != http.StatusUnauthorized {
				t.Fatalf("called = %v, status = %d", called, response.Code)
			}
			if got := response.Header().Get("WWW-Authenticate"); !strings.Contains(got, `resource_metadata="https://rendercase.example.com/.well-known/oauth-protected-resource/mcp"`) {
				t.Fatalf("WWW-Authenticate = %q", got)
			}
		})
	}
}

func TestRequireBearerOIDCStillChallengesMissingAuthorizationHeader(t *testing.T) {
	publicURL, err := url.Parse("https://rendercase.example.com")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: config.Config{AuthMode: config.AuthModeOIDC, PublicURL: publicURL}}
	response := httptest.NewRecorder()

	s.requireBearer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unauthenticated request reached MCP handler")
	})).ServeHTTP(response, httptest.NewRequest(http.MethodPost, publicURL.JoinPath("mcp").String(), nil))
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "bearer token required") {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("WWW-Authenticate"); !strings.Contains(got, `resource_metadata="https://rendercase.example.com/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("WWW-Authenticate = %q", got)
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

func TestRequireBearerCloudflareAccessSwitchboardDelegation(t *testing.T) {
	const issuer = "https://example.cloudflareaccess.com"
	const audience = "access-audience"
	const switchboardClientID = "switchboard.access"
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	publicURL, err := url.Parse("https://rendercase.example.com")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg:        config.Config{AuthMode: config.AuthModeCloudflareAccess, PublicURL: publicURL, SwitchboardAccessClientID: switchboardClientID},
		cfVerifier: oidc.NewVerifier(issuer, rsaKeySet{publicKey: &key.PublicKey}, &oidc.Config{ClientID: audience}),
		cloudflareUserUpsert: func(_ context.Context, user store.User) (store.User, error) {
			user.ID = "upserted:" + user.Subject
			return user, nil
		},
		userBySubject: func(_ context.Context, subject string) (store.User, error) {
			if subject != "cloudflare_access:alice-subject" {
				return store.User{}, store.ErrNotFound
			}
			return store.User{ID: "alice", Subject: subject, Admin: true}, nil
		},
	}
	serviceToken := func(clientID string) string {
		return signAccessJWT(t, signer, issuer, audience, map[string]any{"sub": "", "common_name": clientID, "type": "app"})
	}
	userToken := signAccessJWT(t, signer, issuer, audience, map[string]any{"sub": "bob-subject", "email": "bob@example.com", "type": "app"})
	serve := func(assertion string, delegated ...string) (store.User, int) {
		request := httptest.NewRequest(http.MethodPost, publicURL.JoinPath("mcp").String(), nil)
		request.Header.Set(cfAccessJWTHeader, assertion)
		for _, value := range delegated {
			request.Header.Add(switchboardAccessSubjectHeader, value)
		}
		response := httptest.NewRecorder()
		var user store.User
		s.requireBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user = currentUser(r)
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(response, request)
		return user, response.Code
	}

	if user, code := serve(serviceToken(switchboardClientID), "cloudflare_access:alice-subject"); code != http.StatusNoContent || user.ID != "alice" || !user.Admin {
		t.Fatalf("delegated user = %+v, status = %d", user, code)
	}
	if user, code := serve(userToken); code != http.StatusNoContent || user.ID != "upserted:cloudflare_access:bob-subject" {
		t.Fatalf("direct user = %+v, status = %d", user, code)
	}
	for name, test := range map[string]struct {
		assertion string
		delegated []string
	}{
		"unknown delegated user":        {serviceToken(switchboardClientID), []string{"cloudflare_access:mallory-subject"}},
		"ambiguous subjects":            {serviceToken(switchboardClientID), []string{"cloudflare_access:alice-subject", "cloudflare_access:bob-subject"}},
		"blank subject":                 {serviceToken(switchboardClientID), []string{" "}},
		"unprefixed subject":            {serviceToken(switchboardClientID), []string{"alice-subject"}},
		"bare prefix":                   {serviceToken(switchboardClientID), []string{"cloudflare_access:"}},
		"service token subject":         {serviceToken(switchboardClientID), []string{"cloudflare_access:service_token:x"}},
		"line break":                    {serviceToken(switchboardClientID), []string{"cloudflare_access:alice-subject\nx"}},
		"untrusted service token":       {serviceToken("other.access"), []string{"cloudflare_access:alice-subject"}},
		"other service token as itself": {serviceToken("other.access"), nil},
		"user token delegating":         {userToken, []string{"cloudflare_access:alice-subject"}},
	} {
		t.Run(name, func(t *testing.T) {
			if user, code := serve(test.assertion, test.delegated...); code != http.StatusUnauthorized {
				t.Fatalf("user = %+v, status = %d", user, code)
			}
		})
	}

	request := httptest.NewRequest(http.MethodPost, publicURL.JoinPath("mcp").String(), nil)
	request.Header.Set(cfAccessJWTHeader, serviceToken(switchboardClientID))
	s.requireBearer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		discoveryOnly, _ := r.Context().Value(discoveryOnlyContextKey{}).(bool)
		if user := currentUser(r); !discoveryOnly || user.ID != "" || user.Admin {
			t.Fatalf("switchboard without subject: user = %+v, discovery only = %v", user, discoveryOnly)
		}
	})).ServeHTTP(httptest.NewRecorder(), request)

	mcpHandler, err := s.mcpHandler()
	if err != nil {
		t.Fatal(err)
	}
	discover := func(assertion, body string) (int, string) {
		request := httptest.NewRequest(http.MethodPost, publicURL.JoinPath("mcp").String(), strings.NewReader(body))
		request.Header.Set(cfAccessJWTHeader, assertion)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		response := httptest.NewRecorder()
		s.requireBearer(mcpHandler).ServeHTTP(response, request)
		return response.Code, response.Body.String()
	}
	for name, test := range map[string]struct {
		body    string
		allowed bool
	}{
		"initialize": {`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"switchboard","version":"1"}}}`, true},
		"ping":       {`{"jsonrpc":"2.0","id":1,"method":"ping"}`, true},
		"tools list": {`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, true},
		"tool call":  {`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"rendercase_list","arguments":{}}}`, false},
		"mixed case": {`{"jsonrpc":"2.0","id":3,"method":"tools/call","Method":"tools/list","params":{"name":"rendercase_list","arguments":{}}}`, false},
		"upper case": {`{"jsonrpc":"2.0","id":3,"method":"tools/call","METHOD":"tools/list","params":{"name":"rendercase_list","arguments":{}}}`, false},
		"resources":  {`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`, false},
		"completion": {`{"jsonrpc":"2.0","id":5,"method":"completion/complete","params":{}}`, false},
	} {
		t.Run("discovery "+name, func(t *testing.T) {
			code, body := discover(serviceToken(switchboardClientID), test.body)
			denied := strings.Contains(body, "must delegate a Rendercase user")
			if code != http.StatusOK || denied == test.allowed {
				t.Fatalf("status = %d, body = %s", code, body)
			}
			if test.allowed && !strings.Contains(body, `"result"`) {
				t.Fatalf("allowed request failed: %s", body)
			}
		})
	}
	if code, body := discover(serviceToken(switchboardClientID), `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Fatalf("notification status = %d, body = %s", code, body)
	}
	if _, body := discover(serviceToken(switchboardClientID), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); !strings.Contains(body, `"rendercase_list"`) || strings.Contains(body, "rendercase_admin_") {
		t.Fatalf("discovery tools = %s", body)
	}
	if code, _ := discover(serviceToken("other.access"), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); code != http.StatusUnauthorized {
		t.Fatalf("untrusted service token discovered tools, status = %d", code)
	}

	s.cfg.SwitchboardAccessClientID = ""
	if code, _ := discover(serviceToken(switchboardClientID), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); code != http.StatusUnauthorized {
		t.Fatalf("discovery accepted while disabled, status = %d", code)
	}
	if _, code := serve(serviceToken(switchboardClientID), "cloudflare_access:alice-subject"); code != http.StatusUnauthorized {
		t.Fatalf("delegation accepted while disabled, status = %d", code)
	}
}

func TestCloudflareAccessBrowserRejectsServiceTokenDelegation(t *testing.T) {
	const issuer = "https://example.cloudflareaccess.com"
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg:        config.Config{AuthMode: config.AuthModeCloudflareAccess, SwitchboardAccessClientID: "switchboard.access"},
		cfVerifier: oidc.NewVerifier(issuer, rsaKeySet{publicKey: &key.PublicKey}, &oidc.Config{ClientID: "aud"}),
	}
	request := httptest.NewRequest(http.MethodGet, "https://rendercase.example.com/", nil)
	request.Header.Set(cfAccessJWTHeader, signAccessJWT(t, signer, issuer, "aud", map[string]any{"sub": "", "common_name": "switchboard.access", "type": "app"}))
	request.Header.Set(switchboardAccessSubjectHeader, "cloudflare_access:alice-subject")
	if _, err := s.browserUser(request); err == nil {
		t.Fatal("browser route accepted Switchboard delegation")
	}
}
