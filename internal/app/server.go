package app

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/kilo666mj/rendercase/internal/blob"
	"github.com/kilo666mj/rendercase/internal/config"
	"github.com/kilo666mj/rendercase/internal/securetoken"
	"github.com/kilo666mj/rendercase/internal/store"
)

//go:embed web/*
var webFS embed.FS

type Server struct {
	cfg            config.Config
	db             *store.DB
	blobs          blob.Store
	oauth          oauth2.Config
	verifier       *oidc.IDTokenVerifier
	accessVerifier *oidc.IDTokenVerifier
	cfVerifier     *oidc.IDTokenVerifier
	tpl            *template.Template
	log            *slog.Logger
}

type userContextKey struct{}
type requestIDContextKey struct{}
type remoteAddressContextKey struct{}

func New(ctx context.Context, cfg config.Config, db *store.DB, blobs blob.Store, logger *slog.Logger) (*Server, error) {
	provider, err := oidc.NewProvider(ctx, cfg.OIDCIssuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	tpl, err := template.ParseFS(webFS, "web/*.html")
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{cfg: cfg, db: db, blobs: blobs, accessVerifier: provider.Verifier(&oidc.Config{SkipClientIDCheck: true}), tpl: tpl, log: logger}
	if cfg.AuthMode == config.AuthModeCloudflareAccess {
		keys := oidc.NewRemoteKeySet(ctx, cfg.CFAccessTeamDomain+"/cdn-cgi/access/certs")
		s.cfVerifier = oidc.NewVerifier(cfg.CFAccessTeamDomain, keys, &oidc.Config{ClientID: cfg.CFAccessAudience})
	} else {
		s.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID})
		s.oauth = oauth2.Config{ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret, Endpoint: provider.Endpoint(), RedirectURL: cfg.OIDCRedirectURL, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mainMux := http.NewServeMux()
	mainMux.HandleFunc("GET /healthz", s.health)
	mainMux.HandleFunc("GET /readyz", s.ready)
	mainMux.HandleFunc("GET /.well-known/oauth-protected-resource", s.oauthProtectedResource)
	mainMux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.oauthProtectedResource)
	mainMux.HandleFunc("GET /static/favicon.svg", s.favicon)
	mainMux.HandleFunc("GET /static/style.css", s.style)
	mainMux.HandleFunc("GET /auth/login", s.login)
	mainMux.HandleFunc("GET /api/v1/auth/oidc/callback", s.callback)
	mainMux.Handle("POST /auth/logout", s.requireUser(http.HandlerFunc(s.logout)))
	mainMux.HandleFunc("GET /s/{token}", s.exchangeShare)
	mainMux.Handle("GET /", s.requireUser(http.HandlerFunc(s.index)))
	mainMux.HandleFunc("GET /a/{artifact}", s.viewer)
	mainMux.Handle("GET /api/v1/artifacts", s.requireUser(http.HandlerFunc(s.listArtifacts)))
	mainMux.Handle("POST /api/v1/artifacts/uploads", s.requireUser(http.HandlerFunc(s.createUpload)))
	mainMux.HandleFunc("PUT /api/v1/uploads/{upload}", s.putUpload)
	mainMux.Handle("POST /api/v1/uploads/{upload}/commit", s.requireUser(http.HandlerFunc(s.commitUpload)))
	mainMux.Handle("POST /api/v1/artifacts/{artifact}/shares", s.requireUser(http.HandlerFunc(s.createShare)))
	mainMux.Handle("GET /api/v1/artifacts/{artifact}/shares", s.requireUser(http.HandlerFunc(s.listShares)))
	mainMux.Handle("DELETE /api/v1/shares/{share}", s.requireUser(http.HandlerFunc(s.revokeShare)))
	mcpHandler := s.requireBearer(http.MaxBytesHandler(s.mcpHandler(), ((s.cfg.MaxBundleBytes+2)/3*4)+(1<<20)))
	mainMux.Handle("POST /mcp", mcpHandler)
	mainMux.Handle("GET /mcp", mcpHandler)
	mainMux.Handle("DELETE /mcp", mcpHandler)

	contentMux := http.NewServeMux()
	contentMux.HandleFunc("GET /healthz", s.health)
	contentMux.HandleFunc("GET /t/{ticket}/{artifact}/{version}/{file...}", s.content)

	return s.recoverer(s.requestMetadata(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h
		}
		switch host {
		case strings.ToLower(s.cfg.PublicURL.Hostname()):
			w.Header().Set("X-Frame-Options", "DENY")
			mainMux.ServeHTTP(w, r)
		case strings.ToLower(s.cfg.ContentURL.Hostname()):
			contentMux.ServeHTTP(w, r)
		default:
			http.Error(w, "unknown host", http.StatusMisdirectedRequest)
		}
	})))
}

func (s *Server) RunMaintenance(ctx context.Context) {
	s.runMaintenance(ctx)
	ticker := time.NewTicker(s.cfg.MaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runMaintenance(ctx)
		}
	}
}

func (s *Server) runMaintenance(ctx context.Context) {
	now := time.Now()
	result, locked, err := s.db.CleanupExpired(ctx, now.Add(-s.cfg.AuditRetention))
	if err != nil {
		s.log.Error("maintenance cleanup failed", "error", err)
		return
	}
	if !locked {
		return
	}
	for _, uploadID := range result.UploadIDs {
		if err := s.blobs.RemoveStage(uploadID); err != nil {
			s.log.Error("remove expired upload stage", "upload", uploadID, "error", err)
		}
	}
	staleStages, err := s.blobs.CleanupStages(now.Add(-2 * s.cfg.UploadTTL))
	if err != nil {
		s.log.Error("remove orphaned upload stages", "error", err)
	}
	s.log.Info("maintenance cleanup complete", "sessions", result.Sessions, "share_sessions", result.ShareSessions, "oidc_states", result.OIDCStates, "uploads", result.Uploads, "audit_events", result.Audits, "orphaned_stages", staleStages)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) style(w http.ResponseWriter, _ *http.Request) {
	b, _ := webFS.ReadFile("web/style.css")
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(b)
}

func (s *Server) favicon(w http.ResponseWriter, _ *http.Request) {
	b, _ := webFS.ReadFile("web/favicon.svg")
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(b)
}

func (s *Server) oauthProtectedResource(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/mcp",
		"authorization_servers":    []string{s.cfg.OIDCIssuer},
		"scopes_supported":         []string{s.cfg.OAuthScope},
		"bearer_methods_supported": []string{"header"},
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuthMode == config.AuthModeCloudflareAccess {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	state, stateHash, err := securetoken.Secret()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	verifier, _, err := securetoken.Secret()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	nonce, _, err := securetoken.Secret()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	returnPath := safeReturn(r.URL.Query().Get("return"))
	if err := s.db.CreateOIDCState(r.Context(), stateHash, verifier, nonce, returnPath, time.Now().Add(10*time.Minute)); err != nil {
		s.fail(w, r, err)
		return
	}
	challenge := base64.RawURLEncoding.EncodeToString(sha256Bytes(verifier))
	url := s.oauth.AuthCodeURL(state, oauth2.AccessTypeOnline, oauth2.SetAuthURLParam("code_challenge", challenge), oauth2.SetAuthURLParam("code_challenge_method", "S256"), oauth2.SetAuthURLParam("nonce", nonce))
	http.Redirect(w, r, url, http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuthMode == config.AuthModeCloudflareAccess {
		http.NotFound(w, r)
		return
	}
	if problem := r.URL.Query().Get("error"); problem != "" {
		http.Error(w, "OIDC login failed: "+problem, http.StatusUnauthorized)
		return
	}
	state := r.URL.Query().Get("state")
	verifier, nonce, returnPath, err := s.db.ConsumeOIDCState(r.Context(), securetoken.Hash(state))
	if err != nil {
		http.Error(w, "invalid or expired login state", http.StatusBadRequest)
		return
	}
	token, err := s.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "provider did not return an ID token", http.StatusUnauthorized)
		return
	}
	idToken, err := s.verifier.Verify(r.Context(), rawID)
	if err != nil {
		http.Error(w, "invalid ID token", http.StatusUnauthorized)
		return
	}
	var claims struct {
		Subject, Nonce, PreferredUsername, Email, Name string `json:"-"`
	}
	var raw map[string]any
	if err := idToken.Claims(&raw); err != nil {
		s.fail(w, r, err)
		return
	}
	claims.Subject = stringClaim(raw, "sub")
	claims.Nonce = stringClaim(raw, "nonce")
	claims.PreferredUsername = stringClaim(raw, "preferred_username")
	claims.Email = stringClaim(raw, "email")
	claims.Name = stringClaim(raw, "name")
	if claims.Subject == "" || claims.Nonce != nonce {
		http.Error(w, "invalid OIDC claims", http.StatusUnauthorized)
		return
	}
	if claims.PreferredUsername == "" {
		claims.PreferredUsername = claims.Email
	}
	if claims.Name == "" {
		claims.Name = claims.PreferredUsername
	}
	_, admin := s.cfg.AdminSubjects[claims.Subject]
	u, err := s.db.UpsertUser(r.Context(), claims.Subject, claims.PreferredUsername, claims.Email, claims.Name, admin)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	plain, hash, err := securetoken.Secret()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	expires := time.Now().Add(s.cfg.SessionTTL)
	if err := s.db.CreateSession(r.Context(), hash, u.ID, expires); err != nil {
		s.fail(w, r, err)
		return
	}
	setCookie(w, "rendercase_session", plain, expires, true)
	http.Redirect(w, r, returnPath, http.StatusFound)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuthMode == config.AuthModeCloudflareAccess {
		http.Redirect(w, r, strings.TrimRight(s.cfg.PublicURL.String(), "/")+"/cdn-cgi/access/logout", http.StatusSeeOther)
		return
	}
	if c, err := r.Cookie("rendercase_session"); err == nil {
		_ = s.db.DeleteSession(r.Context(), securetoken.Hash(c.Value))
	}
	setCookie(w, "rendercase_session", "", time.Unix(0, 0), true)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u store.User
		var err error
		bearer := strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
		if bearer {
			u, err = s.bearerUser(r)
		} else {
			u, err = s.browserUser(r)
		}
		if err != nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			if r.URL.Path == "/" && s.cfg.AuthMode == config.AuthModeOIDC {
				_ = s.tpl.ExecuteTemplate(w, "login", nil)
				return
			}
			if s.cfg.AuthMode == config.AuthModeCloudflareAccess {
				writeError(w, http.StatusUnauthorized, "Cloudflare Access authentication required")
			} else {
				http.Redirect(w, r, "/auth/login?return="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			}
			return
		}
		if !bearer && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !s.validOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-site request rejected")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, u)))
	})
}

func (s *Server) validOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Scheme, s.cfg.PublicURL.Scheme) && strings.EqualFold(u.Host, s.cfg.PublicURL.Host)
}

func (s *Server) sessionUser(r *http.Request) (store.User, error) {
	c, err := r.Cookie("rendercase_session")
	if err != nil {
		return store.User{}, store.ErrNotFound
	}
	return s.db.UserBySession(r.Context(), securetoken.Hash(c.Value))
}

func (s *Server) browserUser(r *http.Request) (store.User, error) {
	if s.cfg.AuthMode == config.AuthModeCloudflareAccess {
		return s.cloudflareAccessUser(r)
	}
	return s.sessionUser(r)
}
func currentUser(r *http.Request) store.User { return r.Context().Value(userContextKey{}).(store.User) }

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	artifacts, err := s.db.ListArtifacts(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.tpl.ExecuteTemplate(w, "index", map[string]any{"User": u, "Artifacts": artifacts})
}

func (s *Server) viewer(w http.ResponseWriter, r *http.Request) {
	artifactID := r.PathValue("artifact")
	var u *store.User
	var v store.Version
	canShare := false
	if session, err := s.browserUser(r); err == nil {
		a, err := s.db.ArtifactForUser(r.Context(), artifactID, session.ID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		v, err = s.db.Version(r.Context(), a.ID, a.LatestVersion)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		u = &session
		canShare = a.Role == "owner"
	} else if c, err := r.Cookie("rendercase_share"); err == nil {
		share, err := s.db.ShareBySession(r.Context(), securetoken.Hash(c.Value))
		if err != nil || share.ArtifactID != artifactID {
			http.NotFound(w, r)
			return
		}
		v, err = s.db.VersionForShare(r.Context(), share.ID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	} else {
		http.Redirect(w, r, "/auth/login?return="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	subject := v.ArtifactID + ":" + strconv.Itoa(v.Version)
	ticket := securetoken.Sign(s.cfg.CookieSecret, subject, time.Now().Add(s.cfg.ViewerTicketTTL))
	source := strings.TrimRight(s.cfg.ContentURL.String(), "/") + "/t/" + ticket + "/" + url.PathEscape(v.ArtifactID) + "/" + strconv.Itoa(v.Version) + "/" + escapePath(v.Entrypoint)
	_ = s.tpl.ExecuteTemplate(w, "viewer", map[string]any{"User": u, "Version": v, "ContentSource": source, "CanShare": canShare})
}

func (s *Server) content(w http.ResponseWriter, r *http.Request) {
	artifactID := r.PathValue("artifact")
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	subject, _, err := securetoken.Verify(s.cfg.CookieSecret, r.PathValue("ticket"), time.Now())
	if err != nil || subject != artifactID+":"+strconv.Itoa(version) {
		http.NotFound(w, r)
		return
	}
	v, err := s.db.Version(r.Context(), artifactID, version)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("file")
	f, info, err := s.blobs.Open(v.ObjectDir, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	ctype := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; media-src 'self' blob:; connect-src 'none'; frame-ancestors "+s.cfg.PublicURL.Scheme+"://"+s.cfg.PublicURL.Host+"; form-action 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	a, err := s.db.ListArtifacts(r.Context(), currentUser(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": a})
}

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	var in struct{ ArtifactID, Title, Entrypoint string }
	if !decodeJSON(w, r, &in) {
		return
	}
	var err error
	in.Title, err = normalizeTitle(in.Title)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Entrypoint == "" {
		in.Entrypoint = "index.html"
	}
	u := currentUser(r)
	if in.ArtifactID != "" {
		a, err := s.db.ArtifactForUser(r.Context(), in.ArtifactID, u.ID)
		if err != nil || a.Role != "owner" {
			writeError(w, 403, "artifact owner access required")
			return
		}
	}
	id, err := securetoken.ID()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	plain, hash, err := securetoken.Secret()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	upload := store.Upload{ID: id, ArtifactID: in.ArtifactID, CreatedBy: u.ID, Title: in.Title, Entrypoint: in.Entrypoint, TokenHash: hash, ExpiresAt: time.Now().Add(s.cfg.UploadTTL)}
	if err = s.db.CreateUpload(r.Context(), upload); err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, u.ID, "", in.ArtifactID, "upload.create", map[string]any{"upload_id": id})
	writeJSON(w, http.StatusCreated, map[string]any{"upload_id": id, "upload_token": plain, "upload_url": strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/api/v1/uploads/" + id, "expires_at": upload.ExpiresAt})
}

func (s *Server) putUpload(w http.ResponseWriter, r *http.Request) {
	plain := uploadToken(r)
	upload, err := s.db.UploadByToken(r.Context(), r.PathValue("upload"), securetoken.Hash(plain))
	if err != nil {
		writeError(w, 404, "upload not found or expired")
		return
	}
	staged, err := s.blobs.StageZIP(r.Context(), upload.ID, upload.Title, upload.Entrypoint, http.MaxBytesReader(w, r.Body, s.cfg.MaxBundleBytes+1))
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	manifest, err := json.Marshal(staged.Manifest)
	if err == nil {
		err = s.db.MarkUploadStaged(r.Context(), upload.ID, securetoken.Hash(plain), manifest, staged.Digest, staged.Bytes)
	}
	if err != nil {
		_ = s.blobs.RemoveStage(upload.ID)
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"manifest_sha256": staged.Digest, "bytes": staged.Bytes, "files": len(staged.Manifest.Files)})
}

func (s *Server) commitUpload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UploadToken string `json:"upload_token"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	u := currentUser(r)
	a, v, err := s.commitForUser(r.Context(), u, r.PathValue("upload"), in.UploadToken)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.audit(r, u.ID, "", a.ID, "artifact.publish", map[string]any{"version": v.Version, "upload_id": r.PathValue("upload")})
	writeJSON(w, http.StatusCreated, map[string]any{"artifact": a, "version": v, "url": strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/a/" + a.ID})
}

func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version   *int       `json:"version"`
		ExpiresAt *time.Time `json:"expires_at"`
		ViewLimit *int       `json:"view_limit"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	u := currentUser(r)
	a, err := s.db.ArtifactForUser(r.Context(), r.PathValue("artifact"), u.ID)
	if err != nil || a.Role != "owner" {
		writeError(w, 403, "artifact owner access required")
		return
	}
	if in.Version == nil {
		version := a.LatestVersion
		in.Version = &version
	}
	if err = validateShareOptions(in.Version, in.ExpiresAt, in.ViewLimit, a.LatestVersion, time.Now()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := securetoken.ID()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	plain, hash, err := securetoken.Secret()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	share := store.Share{ID: id, ArtifactID: a.ID, Version: in.Version, CreatedBy: u.ID, ExpiresAt: in.ExpiresAt, ViewLimit: in.ViewLimit}
	if err = s.db.CreateShare(r.Context(), share, hash); err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, u.ID, "", a.ID, "share.create", map[string]any{"share_id": id, "version": in.Version, "expires_at": in.ExpiresAt, "view_limit": in.ViewLimit})
	writeJSON(w, http.StatusCreated, map[string]any{"share_id": id, "url": strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/s/" + plain})
}

func (s *Server) listShares(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	a, err := s.db.ArtifactForUser(r.Context(), r.PathValue("artifact"), u.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	if a.Role != "owner" {
		writeError(w, http.StatusForbidden, "artifact owner access required")
		return
	}
	shares, err := s.db.ListShares(r.Context(), a.ID, u.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "artifact not found")
			return
		}
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": shares})
}
func (s *Server) exchangeShare(w http.ResponseWriter, r *http.Request) {
	plain, hash, err := securetoken.Secret()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	expires := time.Now().Add(s.cfg.SessionTTL)
	share, err := s.db.ExchangeShare(r.Context(), securetoken.Hash(r.PathValue("token")), hash, expires)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.audit(r, "", share.ID, share.ArtifactID, "share.view", map[string]any{"view_count": share.ViewCount})
	setCookie(w, "rendercase_share", plain, expires, true)
	http.Redirect(w, r, "/a/"+share.ArtifactID, http.StatusFound)
}
func (s *Server) revokeShare(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	shareID := r.PathValue("share")
	artifactID, err := s.db.RevokeShare(r.Context(), shareID, u.ID)
	if err != nil {
		writeError(w, 404, "share not found")
		return
	}
	s.audit(r, u.ID, "", artifactID, "share.revoke", map[string]any{"share_id": shareID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requestMetadata(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := securetoken.ID()
		if err != nil {
			s.fail(w, r, err)
			return
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, id)
		ctx = context.WithValue(ctx, remoteAddressContextKey{}, s.clientAddress(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) audit(r *http.Request, userID, shareID, artifactID, action string, details any) {
	s.auditContext(r.Context(), userID, shareID, artifactID, action, details)
}

func (s *Server) auditContext(ctx context.Context, userID, shareID, artifactID, action string, details any) {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	remote, _ := ctx.Value(remoteAddressContextKey{}).(netip.Addr)
	if err := s.db.Audit(ctx, userID, shareID, artifactID, action, requestID, remote, details); err != nil {
		s.log.Error("write audit event", "action", action, "artifact", artifactID, "error", err)
	}
}

func remoteAddress(value string) netip.Addr {
	host, _, err := net.SplitHostPort(value)
	if err == nil {
		value = host
	}
	ip, _ := netip.ParseAddr(value)
	return ip.Unmap()
}

func (s *Server) clientAddress(r *http.Request) netip.Addr {
	peer := remoteAddress(r.RemoteAddr)
	trusted := false
	for _, prefix := range s.cfg.TrustProxyCIDRs {
		if prefix.Contains(peer) {
			trusted = true
			break
		}
	}
	if !trusted {
		return peer
	}
	for _, value := range []string{r.Header.Get("CF-Connecting-IP"), strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]} {
		if addr, err := netip.ParseAddr(strings.TrimSpace(value)); err == nil {
			return addr.Unmap()
		}
	}
	return peer
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("request panic", "error", recovered, "path", r.URL.Path)
				http.Error(w, "internal server error", 500)
			}
		}()
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, 500, "internal server error")
}
func setCookie(w http.ResponseWriter, name, value string, expires time.Time, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Expires: expires, MaxAge: int(time.Until(expires).Seconds()), Secure: true, HttpOnly: httpOnly, SameSite: http.SameSiteLaxMode})
}
func uploadToken(r *http.Request) string {
	if v := r.Header.Get("X-Rendercase-Upload-Token"); v != "" {
		return v
	}
	if v := r.Header.Get("Authorization"); strings.HasPrefix(v, "Upload ") {
		return strings.TrimPrefix(v, "Upload ")
	}
	return ""
}
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func safeReturn(v string) string {
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.ContainsAny(v, "\\\r\n\t") {
		return "/"
	}
	return v
}

const maxTitleBytes = 200

func normalizeTitle(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("title is required")
	}
	if len(value) > maxTitleBytes {
		return "", fmt.Errorf("title must not exceed %d bytes", maxTitleBytes)
	}
	return value, nil
}

func validateShareOptions(version *int, expiresAt *time.Time, viewLimit *int, latestVersion int, now time.Time) error {
	if version == nil || *version < 1 || *version > latestVersion {
		return errors.New("version must identify an existing artifact version")
	}
	if viewLimit != nil && *viewLimit < 1 {
		return errors.New("view_limit must be positive")
	}
	if expiresAt != nil && !expiresAt.After(now) {
		return errors.New("expires_at must be in the future")
	}
	return nil
}
func stringClaim(m map[string]any, k string) string { v, _ := m[k].(string); return v }
func sha256Bytes(v string) []byte                   { sum := sha256.Sum256([]byte(v)); return sum[:] }
func escapePath(v string) string {
	parts := strings.Split(v, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
func pathJoinFS(parts ...string) string { return strings.Join(parts, "/") }
