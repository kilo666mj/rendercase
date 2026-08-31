package app

import (
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
	identity, err := s.verifyCloudflareAccess(r)
	if err != nil {
		return store.User{}, err
	}
	admin := s.cloudflareAccessAdmin(identity)
	return s.db.UpsertUser(r.Context(), "cloudflare_access:"+identity.Subject, identity.Username, identity.Email, identity.Name, admin)
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
	raw := r.Header.Get(cfAccessJWTHeader)
	if raw == "" {
		return cloudflareAccessIdentity{}, errors.New("Cloudflare Access JWT is required")
	}
	if s.cfVerifier == nil {
		return cloudflareAccessIdentity{}, errors.New("Cloudflare Access verifier is unavailable")
	}
	token, err := s.cfVerifier.Verify(r.Context(), raw)
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
	if identity.Subject == "" || identity.Email == "" {
		return cloudflareAccessIdentity{}, errors.New("Cloudflare Access JWT requires sub and email claims")
	}
	identity.Username = identity.Email
	if identity.Name == "" {
		identity.Name = identity.Email
	}
	return identity, nil
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
