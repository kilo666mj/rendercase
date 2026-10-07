package app

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/kilo666mj/rendercase/internal/store"
)

const cfAccessJWTHeader = "Cf-Access-Jwt-Assertion"

type cloudflareAccessIdentity struct {
	Subject, Username, Email, Name string
	Groups                         []string
}

func (s *Server) cloudflareAccessUser(r *http.Request) (store.User, error) {
	return s.cloudflareAccessUserFromJWT(r.Context(), r.Header.Get(cfAccessJWTHeader))
}

func (s *Server) cloudflareAccessUserFromJWT(ctx context.Context, raw string) (store.User, error) {
	identity, err := s.verifyCloudflareAccessJWT(ctx, raw)
	if err != nil {
		return store.User{}, err
	}
	return s.upsertCloudflareAccessUser(ctx, identity)
}

// cloudflareAccessMCPUser authenticates an MCP request. Switchboard's Access
// service token may act for an existing Rendercase user named in
// X-Switchboard-Access-Subject; every other caller acts as itself.
func (s *Server) cloudflareAccessMCPUser(r *http.Request) (store.User, error) {
	claims, err := s.verifyCloudflareAccessClaims(r.Context(), r.Header.Get(cfAccessJWTHeader))
	if err != nil {
		return store.User{}, err
	}
	values := r.Header.Values(switchboardAccessSubjectHeader)
	clientID := cloudflareAccessServiceTokenClientID(claims)
	if clientID == "" {
		if len(values) != 0 {
			return store.User{}, errors.New("authenticated identity cannot delegate a Rendercase user")
		}
		identity, err := cloudflareAccessIdentityFromClaims(claims)
		if err != nil {
			return store.User{}, err
		}
		return s.upsertCloudflareAccessUser(r.Context(), identity)
	}
	subject, delegated, err := resolveMCPSubject(clientID, s.cfg.SwitchboardAccessClientID, values)
	if err != nil {
		return store.User{}, err
	}
	if !delegated {
		return store.User{}, errors.New("Cloudflare Access service token requires a delegated Rendercase user")
	}
	return s.delegatedUser(r.Context(), "cloudflare_access:"+subject)
}

// cloudflareAccessServiceTokenClientID returns the client ID of a service-token
// assertion, which carries common_name and no user subject.
func cloudflareAccessServiceTokenClientID(claims map[string]any) string {
	if stringClaim(claims, "sub") != "" {
		return ""
	}
	return stringClaim(claims, "common_name")
}

func (s *Server) upsertCloudflareAccessUser(ctx context.Context, identity cloudflareAccessIdentity) (store.User, error) {
	user := store.User{
		Subject:     "cloudflare_access:" + identity.Subject,
		Username:    identity.Username,
		Email:       identity.Email,
		DisplayName: identity.Name,
		Admin:       s.cloudflareAccessAdmin(identity),
	}
	if s.cloudflareUserUpsert != nil {
		return s.cloudflareUserUpsert(ctx, user)
	}
	return s.db.UpsertUser(ctx, user.Subject, user.Username, user.Email, user.DisplayName, user.Admin)
}

func (s *Server) cloudflareAccessAdmin(identity cloudflareAccessIdentity) bool {
	_, admin := s.cfg.AdminSubjects[identity.Subject]
	if !admin {
		for group := range s.cfg.AdminGroups {
			if slices.Contains(identity.Groups, group) {
				admin = true
				break
			}
		}
	}
	return admin
}

func (s *Server) verifyCloudflareAccess(r *http.Request) (cloudflareAccessIdentity, error) {
	return s.verifyCloudflareAccessJWT(r.Context(), r.Header.Get(cfAccessJWTHeader))
}

func (s *Server) verifyCloudflareAccessJWT(ctx context.Context, raw string) (cloudflareAccessIdentity, error) {
	claims, err := s.verifyCloudflareAccessClaims(ctx, raw)
	if err != nil {
		return cloudflareAccessIdentity{}, err
	}
	return cloudflareAccessIdentityFromClaims(claims)
}

func (s *Server) verifyCloudflareAccessClaims(ctx context.Context, raw string) (map[string]any, error) {
	if raw == "" {
		return nil, errors.New("missing Cloudflare Access JWT")
	}
	if s.cfVerifier == nil {
		return nil, errors.New("missing Cloudflare Access verifier")
	}
	token, err := s.cfVerifier.Verify(ctx, raw)
	if err != nil {
		return nil, errors.New("invalid Cloudflare Access JWT")
	}
	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		return nil, errors.New("invalid Cloudflare Access claims")
	}
	if stringClaim(claims, "type") != "app" {
		return nil, errors.New("expected Cloudflare Access application token")
	}
	return claims, nil
}

func cloudflareAccessIdentityFromClaims(claims map[string]any) (cloudflareAccessIdentity, error) {
	identity := cloudflareAccessIdentity{
		Subject: stringClaim(claims, "sub"),
		Email:   stringClaim(claims, "email"),
		Name:    stringClaim(claims, "name"),
		Groups:  stringSliceClaim(claims["groups"]),
	}
	if custom, ok := claims["custom"].(map[string]any); ok {
		identity.Groups = appendUnique(identity.Groups, stringSliceClaim(custom["groups"])...)
	}
	if identity.Subject == "" || identity.Email == "" {
		return cloudflareAccessIdentity{}, errors.New("missing Cloudflare Access JWT sub or email claim")
	}
	identity.Username = identity.Email
	if identity.Name == "" {
		identity.Name = identity.Email
	}
	return identity, nil
}

func appendUnique(values []string, additions ...string) []string {
	for _, addition := range additions {
		if !slices.Contains(values, addition) {
			values = append(values, addition)
		}
	}
	return values
}

func stringSliceClaim(value any) []string {
	switch value := value.(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	case string:
		if value != "" {
			return []string{value}
		}
	}
	return nil
}
