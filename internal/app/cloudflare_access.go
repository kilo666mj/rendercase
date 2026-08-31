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
	admin := s.cloudflareAccessAdmin(identity)
	return s.db.UpsertUser(ctx, "cloudflare_access:"+identity.Subject, identity.Username, identity.Email, identity.Name, admin)
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
	if raw == "" {
		return cloudflareAccessIdentity{}, errors.New("Cloudflare Access JWT is required")
	}
	if s.cfVerifier == nil {
		return cloudflareAccessIdentity{}, errors.New("Cloudflare Access verifier is unavailable")
	}
	token, err := s.cfVerifier.Verify(ctx, raw)
	if err != nil {
		return cloudflareAccessIdentity{}, errors.New("invalid Cloudflare Access JWT")
	}
	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		return cloudflareAccessIdentity{}, errors.New("invalid Cloudflare Access claims")
	}
	identity := cloudflareAccessIdentity{
		Subject: stringClaim(claims, "sub"),
		Email:   stringClaim(claims, "email"),
		Name:    stringClaim(claims, "name"),
		Groups:  stringSliceClaim(claims["groups"]),
	}
	if custom, ok := claims["custom"].(map[string]any); ok {
		identity.Groups = appendUnique(identity.Groups, stringSliceClaim(custom["groups"])...)
	}
	if stringClaim(claims, "type") != "app" {
		return cloudflareAccessIdentity{}, errors.New("Cloudflare Access JWT must be an application token")
	}
	if identity.Subject == "" || identity.Email == "" {
		return cloudflareAccessIdentity{}, errors.New("Cloudflare Access JWT requires sub and email claims")
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
