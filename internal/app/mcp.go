package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kilo666mj/mcpkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kilo666mj/rendercase/internal/blob"
	"github.com/kilo666mj/rendercase/internal/config"
	"github.com/kilo666mj/rendercase/internal/securetoken"
	"github.com/kilo666mj/rendercase/internal/store"
)

func (s *Server) mcpHandler() (http.Handler, error) {
	return mcpkit.StatelessHTTP(func(r *http.Request) *mcp.Server {
		return s.newMCPServer(currentUser(r))
	}, mcpkit.HTTPOptions{
		MaxRequestBodyBytes: ((s.cfg.MaxBundleBytes + 2) / 3 * 4) + (1 << 20),
		Logger:              s.log,
	})
}

func (s *Server) requireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.mcpBearerUser(r)
		if err != nil {
			if s.cfg.AuthMode != config.AuthModeCloudflareAccess && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				w.Header().Set("WWW-Authenticate", `Bearer realm="rendercase-mcp", error="invalid_token", resource_metadata="`+strings.TrimRight(s.cfg.PublicURL.String(), "/")+`/.well-known/oauth-protected-resource/mcp"`)
			}
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, u)))
	})
}

func (s *Server) mcpBearerUser(r *http.Request) (store.User, error) {
	if s.cfg.AuthMode == config.AuthModeCloudflareAccess {
		return s.cloudflareAccessUser(r)
	}
	return s.bearerUser(r)
}

func (s *Server) bearerUser(r *http.Request) (store.User, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return store.User{}, errors.New("bearer token required")
	}
	token, err := s.accessVerifier.Verify(r.Context(), strings.TrimPrefix(header, "Bearer "))
	if err != nil {
		return store.User{}, errors.New("invalid bearer token")
	}
	var claims map[string]any
	if err = token.Claims(&claims); err != nil || stringClaim(claims, "sub") == "" {
		return store.User{}, errors.New("invalid bearer claims")
	}
	if !audienceAllowed(token.Audience, s.cfg.OAuthAudiences) {
		return store.User{}, errors.New("token audience is not authorized for Rendercase")
	}
	if !slices.Contains(scopeClaim(claims["scope"]), s.cfg.OAuthScope) {
		return store.User{}, errors.New("token is missing the Rendercase MCP scope")
	}
	subject := stringClaim(claims, "sub")
	username := stringClaim(claims, "preferred_username")
	email := stringClaim(claims, "email")
	name := stringClaim(claims, "name")
	if username == "" {
		username = email
	}
	if name == "" {
		name = username
	}
	_, admin := s.cfg.AdminSubjects[subject]
	return s.db.UpsertUser(r.Context(), subject, username, email, name, admin)
}

func audienceAllowed(tokenAudiences, allowed []string) bool {
	for _, audience := range tokenAudiences {
		if slices.Contains(allowed, audience) {
			return true
		}
	}
	return false
}

func scopeClaim(value any) []string {
	switch v := value.(type) {
	case string:
		return strings.Fields(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if scope, ok := item.(string); ok {
				out = append(out, scope)
			}
		}
		return out
	default:
		return nil
	}
}

type listInput struct{}
type listOutput struct {
	Artifacts []store.Artifact `json:"artifacts"`
}
type getInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"required"`
	Version    int    `json:"version,omitempty"`
}
type getOutput struct {
	Artifact store.Artifact `json:"artifact"`
	Version  mcpVersion     `json:"version"`
	URL      string         `json:"url"`
}
type mcpVersion struct {
	ArtifactID, Title, Entrypoint, ObjectDir, ManifestSHA256 string
	Version, FileCount                                       int
	ByteSize                                                 int64
	Manifest                                                 map[string]any
	CreatedBy                                                string
	CreatedAt                                                time.Time
}
type createUploadInput struct {
	Title      string `json:"title" jsonschema:"required,artifact title"`
	Entrypoint string `json:"entrypoint,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
}
type createUploadOutput struct {
	UploadID, UploadToken, UploadURL string
	ExpiresAt                        time.Time `json:"expires_at"`
}
type commitInput struct {
	UploadID    string `json:"upload_id" jsonschema:"required"`
	UploadToken string `json:"upload_token" jsonschema:"required"`
}
type commitOutput struct {
	Artifact store.Artifact `json:"artifact"`
	Version  mcpVersion     `json:"version"`
	URL      string         `json:"url"`
}
type publishInput struct {
	Title        string `json:"title" jsonschema:"required,artifact title"`
	Entrypoint   string `json:"entrypoint,omitempty"`
	ArtifactID   string `json:"artifact_id,omitempty"`
	BundleBase64 string `json:"bundle_base64" jsonschema:"required,base64-encoded ZIP bundle"`
}
type shareInput struct {
	ArtifactID string     `json:"artifact_id" jsonschema:"required"`
	Version    *int       `json:"version,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	ViewLimit  *int       `json:"view_limit,omitempty"`
}
type shareOutput struct{ ShareID, URL string }
type revokeShareInput struct {
	ShareID string `json:"share_id" jsonschema:"required"`
}
type revokeShareOutput struct {
	ArtifactID string `json:"artifact_id"`
}
type visibilityInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"required"`
	Visibility string `json:"visibility" jsonschema:"required,private or authenticated"`
}
type adminListOutput struct {
	Artifacts []store.AdminArtifact `json:"artifacts"`
}
type adminDeleteArtifactInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"required"`
}

func (s *Server) newMCPServer(user store.User) *mcp.Server {
	server := mcpkit.MustServer(mcpkit.ServerConfig{Name: "rendercase", Version: "0.1.0", Logger: s.log})
	mcp.AddTool(server, &mcp.Tool{Name: "rendercase_list", Description: "List artifacts the authenticated user owns or can access.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listInput) (*mcp.CallToolResult, listOutput, error) {
		artifacts, err := s.db.ListArtifacts(ctx, user.ID)
		if err != nil {
			return nil, listOutput{}, err
		}
		out := listOutput{Artifacts: artifacts}
		return textResult(fmt.Sprintf("Found %d artifacts.", len(artifacts))), out, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "rendercase_get", Description: "Get an accessible artifact and one immutable version. Version 0 selects the latest.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, _ *mcp.CallToolRequest, in getInput) (*mcp.CallToolResult, getOutput, error) {
		a, err := s.db.ArtifactForUser(ctx, in.ArtifactID, user.ID)
		if err != nil {
			return nil, getOutput{}, errors.New("artifact access required")
		}
		if in.Version == 0 {
			in.Version = a.LatestVersion
		}
		v, err := s.db.Version(ctx, a.ID, in.Version)
		if err != nil {
			return nil, getOutput{}, errors.New("artifact version not found")
		}
		mcpV, err := versionForMCP(v)
		if err != nil {
			return nil, getOutput{}, err
		}
		out := getOutput{Artifact: a, Version: mcpV, URL: strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/a/" + a.ID}
		return textResult("Found " + a.Title + " version " + strconv.Itoa(v.Version) + "."), out, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "rendercase_create_upload", Description: "Create a short-lived upload URL for a ZIP bundle. To update an existing artifact, set artifact_id to an artifact owned by the caller; committing creates its next immutable version. PUT the ZIP using X-Rendercase-Upload-Token, then call rendercase_commit_upload.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, _ *mcp.CallToolRequest, in createUploadInput) (*mcp.CallToolResult, createUploadOutput, error) {
		var err error
		in.Title, err = normalizeTitle(in.Title)
		if err != nil {
			return nil, createUploadOutput{}, err
		}
		if in.Entrypoint == "" {
			in.Entrypoint = "index.html"
		}
		if in.ArtifactID != "" {
			a, err := s.db.ArtifactForUser(ctx, in.ArtifactID, user.ID)
			if err != nil || a.Role != "owner" {
				return nil, createUploadOutput{}, errors.New("artifact owner access required")
			}
		}
		id, err := securetoken.ID()
		if err != nil {
			return nil, createUploadOutput{}, err
		}
		plain, hash, err := securetoken.Secret()
		if err != nil {
			return nil, createUploadOutput{}, err
		}
		expires := time.Now().Add(s.cfg.UploadTTL)
		if err = s.db.CreateUpload(ctx, store.Upload{ID: id, ArtifactID: in.ArtifactID, CreatedBy: user.ID, Title: in.Title, Entrypoint: in.Entrypoint, TokenHash: hash, ExpiresAt: expires}); err != nil {
			return nil, createUploadOutput{}, err
		}
		s.auditContext(ctx, user.ID, "", in.ArtifactID, "upload.create", map[string]any{"upload_id": id, "interface": "mcp"})
		out := createUploadOutput{UploadID: id, UploadToken: plain, UploadURL: s.uploadURL(id), ExpiresAt: expires}
		return textResult("Upload URL created. PUT the ZIP bundle, then commit upload " + id + "."), out, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "rendercase_commit_upload", Description: "Commit a staged ZIP upload as a new immutable artifact version.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, _ *mcp.CallToolRequest, in commitInput) (*mcp.CallToolResult, commitOutput, error) {
		a, v, err := s.commitForUser(ctx, user, in.UploadID, in.UploadToken)
		if err != nil {
			return nil, commitOutput{}, err
		}
		s.auditContext(ctx, user.ID, "", a.ID, "artifact.publish", map[string]any{"version": v.Version, "upload_id": in.UploadID, "interface": "mcp"})
		mcpV, err := versionForMCP(v)
		if err != nil {
			return nil, commitOutput{}, err
		}
		out := commitOutput{Artifact: a, Version: mcpV, URL: strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/a/" + a.ID}
		return textResult("Published " + a.Title + " version " + strconv.Itoa(v.Version) + ": " + out.URL), out, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "rendercase_publish", Description: "Create or update an artifact in one MCP call from a base64-encoded ZIP bundle. Set artifact_id to an artifact owned by the caller to create its next immutable version.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, _ *mcp.CallToolRequest, in publishInput) (*mcp.CallToolResult, commitOutput, error) {
		out, err := s.publishBundleForUser(ctx, user, in)
		if err != nil {
			return nil, commitOutput{}, err
		}
		return textResult("Published " + out.Artifact.Title + " version " + strconv.Itoa(out.Version.Version) + ": " + out.URL), out, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "rendercase_share", Description: "Create a revocable capability link for an artifact. Anyone with the link can view it until it expires or is revoked.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, _ *mcp.CallToolRequest, in shareInput) (*mcp.CallToolResult, shareOutput, error) {
		a, err := s.db.ArtifactForUser(ctx, in.ArtifactID, user.ID)
		if err != nil || a.Role != "owner" {
			return nil, shareOutput{}, errors.New("artifact owner access required")
		}
		if in.Version == nil {
			version := a.LatestVersion
			in.Version = &version
		}
		if err = validateShareOptions(in.Version, in.ExpiresAt, in.ViewLimit, a.LatestVersion, time.Now()); err != nil {
			return nil, shareOutput{}, err
		}
		id, err := securetoken.ID()
		if err != nil {
			return nil, shareOutput{}, err
		}
		plain, hash, err := securetoken.Secret()
		if err != nil {
			return nil, shareOutput{}, err
		}
		if err = s.db.CreateShare(ctx, store.Share{ID: id, ArtifactID: a.ID, Version: in.Version, CreatedBy: user.ID, ExpiresAt: in.ExpiresAt, ViewLimit: in.ViewLimit}, hash); err != nil {
			return nil, shareOutput{}, err
		}
		s.auditContext(ctx, user.ID, "", a.ID, "share.create", map[string]any{"share_id": id, "version": in.Version, "expires_at": in.ExpiresAt, "view_limit": in.ViewLimit, "interface": "mcp"})
		out := shareOutput{ShareID: id, URL: strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/s/" + plain}
		return textResult("Capability link created: " + out.URL), out, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "rendercase_set_visibility", Description: "Set an owned artifact to private or make it visible to every authenticated Rendercase account.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, _ *mcp.CallToolRequest, in visibilityInput) (*mcp.CallToolResult, store.Artifact, error) {
		a, err := s.db.SetArtifactVisibility(ctx, in.ArtifactID, user.ID, in.Visibility)
		if err != nil {
			return nil, store.Artifact{}, err
		}
		s.auditContext(ctx, user.ID, "", a.ID, "artifact.visibility.update", map[string]any{"visibility": a.Visibility, "interface": "mcp"})
		return textResult("Artifact visibility set to " + a.Visibility + "."), a, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "rendercase_revoke_share", Description: "Immediately revoke a capability share owned by the authenticated user.", Annotations: mcpkit.Destructive(false, false)}, func(ctx context.Context, _ *mcp.CallToolRequest, in revokeShareInput) (*mcp.CallToolResult, revokeShareOutput, error) {
		artifactID, err := s.db.RevokeShare(ctx, in.ShareID, user.ID)
		if err != nil {
			return nil, revokeShareOutput{}, errors.New("share not found")
		}
		s.auditContext(ctx, user.ID, "", artifactID, "share.revoke", map[string]any{"share_id": in.ShareID, "interface": "mcp"})
		return textResult("Capability share revoked."), revokeShareOutput{ArtifactID: artifactID}, nil
	})
	if user.Admin {
		mcp.AddTool(server, &mcp.Tool{Name: "rendercase_admin_list", Description: "List every active artifact, its owner, and active capability shares. Administrator access required.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listInput) (*mcp.CallToolResult, adminListOutput, error) {
			artifacts, err := s.db.ListAdminArtifacts(ctx)
			if err != nil {
				return nil, adminListOutput{}, err
			}
			return textResult(fmt.Sprintf("Found %d active artifacts across all users.", len(artifacts))), adminListOutput{Artifacts: artifacts}, nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "rendercase_admin_revoke_share", Description: "Revoke any capability share. Administrator access required; the action is audited.", Annotations: mcpkit.Destructive(false, false)}, func(ctx context.Context, _ *mcp.CallToolRequest, in revokeShareInput) (*mcp.CallToolResult, revokeShareOutput, error) {
			artifactID, err := s.db.AdminRevokeShare(ctx, in.ShareID)
			if err != nil {
				return nil, revokeShareOutput{}, errors.New("share not found")
			}
			s.auditContext(ctx, user.ID, "", artifactID, "admin.share.revoke", map[string]any{"share_id": in.ShareID, "interface": "mcp"})
			return textResult("Capability share revoked by administrator."), revokeShareOutput{ArtifactID: artifactID}, nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "rendercase_admin_delete_artifact", Description: "Soft-delete any artifact and immediately revoke all of its shares. Stored files remain available for operator recovery. Administrator access required; the action is audited.", Annotations: mcpkit.Destructive(false, false)}, func(ctx context.Context, _ *mcp.CallToolRequest, in adminDeleteArtifactInput) (*mcp.CallToolResult, revokeShareOutput, error) {
			artifact, err := s.db.AdminDeleteArtifact(ctx, in.ArtifactID)
			if err != nil {
				return nil, revokeShareOutput{}, errors.New("artifact not found")
			}
			s.auditContext(ctx, user.ID, "", artifact.ID, "admin.artifact.delete", map[string]any{"owner_id": artifact.OwnerID, "owner_email": artifact.OwnerEmail, "title": artifact.Title, "interface": "mcp"})
			return textResult("Artifact removed by administrator and all capability shares revoked."), revokeShareOutput{ArtifactID: artifact.ID}, nil
		})
	}
	return server
}

func (s *Server) publishBundleForUser(ctx context.Context, user store.User, in publishInput) (commitOutput, error) {
	var err error
	in.Title, err = normalizeTitle(in.Title)
	if err != nil {
		return commitOutput{}, err
	}
	if in.Entrypoint == "" {
		in.Entrypoint = "index.html"
	}
	if in.ArtifactID != "" {
		a, err := s.db.ArtifactForUser(ctx, in.ArtifactID, user.ID)
		if err != nil || a.Role != "owner" {
			return commitOutput{}, errors.New("artifact owner access required")
		}
	}
	maxEncodedBytes := ((s.cfg.MaxBundleBytes + 2) / 3 * 4) + 1024
	if int64(len(in.BundleBase64)) > maxEncodedBytes {
		return commitOutput{}, fmt.Errorf("encoded bundle exceeds %d bytes", maxEncodedBytes)
	}
	bundle, err := base64.StdEncoding.DecodeString(in.BundleBase64)
	if err != nil {
		return commitOutput{}, errors.New("bundle_base64 is not valid base64")
	}
	if int64(len(bundle)) > s.cfg.MaxBundleBytes {
		return commitOutput{}, fmt.Errorf("bundle exceeds %d bytes", s.cfg.MaxBundleBytes)
	}
	id, err := securetoken.ID()
	if err != nil {
		return commitOutput{}, err
	}
	plain, hash, err := securetoken.Secret()
	if err != nil {
		return commitOutput{}, err
	}
	upload := store.Upload{ID: id, ArtifactID: in.ArtifactID, CreatedBy: user.ID, Title: in.Title, Entrypoint: in.Entrypoint, TokenHash: hash, ExpiresAt: time.Now().Add(s.cfg.UploadTTL)}
	if err = s.db.CreateUpload(ctx, upload); err != nil {
		return commitOutput{}, err
	}
	s.auditContext(ctx, user.ID, "", in.ArtifactID, "upload.create", map[string]any{"upload_id": id, "interface": "mcp-inline"})
	staged, err := s.blobs.StageZIP(ctx, id, in.Title, in.Entrypoint, bytes.NewReader(bundle))
	if err != nil {
		return commitOutput{}, err
	}
	manifest, err := json.Marshal(staged.Manifest)
	if err == nil {
		err = s.db.MarkUploadStaged(ctx, id, hash, manifest, staged.Digest, staged.Bytes)
	}
	if err != nil {
		_ = s.blobs.RemoveStage(id)
		return commitOutput{}, err
	}
	a, v, err := s.commitForUser(ctx, user, id, plain)
	if err != nil {
		return commitOutput{}, err
	}
	s.auditContext(ctx, user.ID, "", a.ID, "artifact.publish", map[string]any{"version": v.Version, "upload_id": id, "interface": "mcp-inline"})
	mcpV, err := versionForMCP(v)
	if err != nil {
		return commitOutput{}, err
	}
	return commitOutput{Artifact: a, Version: mcpV, URL: strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/a/" + a.ID}, nil
}

func versionForMCP(v store.Version) (mcpVersion, error) {
	manifest := make(map[string]any)
	if err := json.Unmarshal(v.Manifest, &manifest); err != nil {
		return mcpVersion{}, fmt.Errorf("decode artifact manifest: %w", err)
	}
	return mcpVersion{
		ArtifactID: v.ArtifactID, Title: v.Title, Entrypoint: v.Entrypoint,
		ObjectDir: v.ObjectDir, ManifestSHA256: v.ManifestSHA256, Version: v.Version,
		FileCount: v.FileCount, ByteSize: v.ByteSize, Manifest: manifest,
		CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt,
	}, nil
}

var (
	errUploadNotFound  = errors.New("upload not found or expired")
	errUploadNotStaged = errors.New("upload has no staged bundle")
)

func (s *Server) commitForUser(ctx context.Context, user store.User, uploadID, uploadToken string) (store.Artifact, store.Version, error) {
	upload, err := s.db.UploadByToken(ctx, uploadID, securetoken.Hash(uploadToken))
	if err != nil || upload.CreatedBy != user.ID {
		return store.Artifact{}, store.Version{}, errUploadNotFound
	}
	if upload.CommittedAt != nil && upload.CommittedVersion != nil && upload.ArtifactID != "" {
		a, artifactErr := s.db.ArtifactForUser(ctx, upload.ArtifactID, user.ID)
		if artifactErr != nil {
			return store.Artifact{}, store.Version{}, artifactErr
		}
		v, versionErr := s.db.Version(ctx, upload.ArtifactID, *upload.CommittedVersion)
		return a, v, versionErr
	}
	if string(upload.StagedManifest) == "null" {
		return store.Artifact{}, store.Version{}, errUploadNotStaged
	}
	var manifest blob.Manifest
	if err = json.Unmarshal(upload.StagedManifest, &manifest); err != nil {
		return store.Artifact{}, store.Version{}, err
	}
	artifactID := upload.ArtifactID
	if artifactID == "" {
		artifactID = "a_" + upload.ID
	}
	staged := s.blobs.Staged(upload.ID, manifest, upload.StagedSHA256, upload.StagedBytes)
	objectDir, err := s.blobs.PublishUpload(ctx, staged, artifactID)
	if err != nil {
		return store.Artifact{}, store.Version{}, err
	}
	return s.db.CommitVersion(ctx, store.CommitInput{UploadID: upload.ID, UserID: user.ID, ArtifactID: upload.ArtifactID, Title: upload.Title, Entrypoint: upload.Entrypoint, ObjectDir: objectDir, Manifest: upload.StagedManifest, ManifestSHA256: upload.StagedSHA256, ByteSize: upload.StagedBytes, FileCount: len(manifest.Files)})
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
