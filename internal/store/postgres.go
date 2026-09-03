package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

var (
	ErrNotFound          = errors.New("not found")
	ErrInvalidVisibility = errors.New("visibility must be private or authenticated")
)

type DB struct{ Pool *pgxpool.Pool }

type CleanupResult struct {
	UploadIDs                                            []string
	Sessions, ShareSessions, OIDCStates, Uploads, Audits int64
}

type User struct {
	ID, Subject, Username, Email, DisplayName string
	Admin                                     bool
}

type Branding struct {
	ThemeName       string `json:"theme_name"`
	SiteName        string `json:"site_name"`
	Tagline         string `json:"tagline"`
	HeroTitle       string `json:"hero_title"`
	HeroHighlight   string `json:"hero_highlight"`
	HeroDescription string `json:"hero_description"`
	BackgroundColor string `json:"background_color"`
	PanelColor      string `json:"panel_color"`
	TextColor       string `json:"text_color"`
	MutedColor      string `json:"muted_color"`
	PrimaryColor    string `json:"primary_color"`
	AccentColor     string `json:"accent_color"`
	LogoMIME        string `json:"logo_mime,omitempty"`
	LogoData        []byte `json:"-"`
}

func (b Branding) HasLogo() bool { return len(b.LogoData) > 0 }

func (d *DB) Branding(ctx context.Context) (Branding, error) {
	var b Branding
	err := d.Pool.QueryRow(ctx, `SELECT theme_name,site_name,tagline,hero_title,hero_highlight,hero_description,
		background_color,panel_color,text_color,muted_color,primary_color,accent_color,
		COALESCE(logo_mime,''),COALESCE(logo_data,''::bytea) FROM instance_branding WHERE singleton=true`).Scan(
		&b.ThemeName, &b.SiteName, &b.Tagline, &b.HeroTitle, &b.HeroHighlight, &b.HeroDescription,
		&b.BackgroundColor, &b.PanelColor, &b.TextColor, &b.MutedColor, &b.PrimaryColor, &b.AccentColor,
		&b.LogoMIME, &b.LogoData)
	return b, err
}

func (d *DB) UpdateBranding(ctx context.Context, b Branding) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO branding_themes(name,site_name,tagline,hero_title,hero_highlight,hero_description,background_color,panel_color,text_color,muted_color,primary_color,accent_color,logo_mime,logo_data)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT(name) DO UPDATE SET site_name=EXCLUDED.site_name,tagline=EXCLUDED.tagline,hero_title=EXCLUDED.hero_title,hero_highlight=EXCLUDED.hero_highlight,hero_description=EXCLUDED.hero_description,background_color=EXCLUDED.background_color,panel_color=EXCLUDED.panel_color,text_color=EXCLUDED.text_color,muted_color=EXCLUDED.muted_color,primary_color=EXCLUDED.primary_color,accent_color=EXCLUDED.accent_color,logo_mime=EXCLUDED.logo_mime,logo_data=EXCLUDED.logo_data,updated_at=now()`, b.ThemeName, b.SiteName, b.Tagline, b.HeroTitle, b.HeroHighlight, b.HeroDescription, b.BackgroundColor, b.PanelColor, b.TextColor, b.MutedColor, b.PrimaryColor, b.AccentColor, nullString(b.LogoMIME), nullBytes(b.LogoData))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE instance_branding SET theme_name=$1,site_name=$2,tagline=$3,hero_title=$4,
		hero_highlight=$5,hero_description=$6,background_color=$7,panel_color=$8,text_color=$9,
		muted_color=$10,primary_color=$11,accent_color=$12,logo_mime=$13,logo_data=$14,updated_at=now()
		WHERE singleton=true`, b.ThemeName, b.SiteName, b.Tagline, b.HeroTitle, b.HeroHighlight, b.HeroDescription,
		b.BackgroundColor, b.PanelColor, b.TextColor, b.MutedColor, b.PrimaryColor, b.AccentColor,
		nullString(b.LogoMIME), nullBytes(b.LogoData))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (d *DB) BrandingThemeNames(ctx context.Context) ([]string, error) {
	rows, err := d.Pool.Query(ctx, `SELECT name FROM branding_themes ORDER BY lower(name),name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}
func (d *DB) ActivateBrandingTheme(ctx context.Context, name string) (Branding, error) {
	var b Branding
	err := d.Pool.QueryRow(ctx, `SELECT name,site_name,tagline,hero_title,hero_highlight,hero_description,background_color,panel_color,text_color,muted_color,primary_color,accent_color,COALESCE(logo_mime,''),COALESCE(logo_data,''::bytea) FROM branding_themes WHERE name=$1`, name).Scan(&b.ThemeName, &b.SiteName, &b.Tagline, &b.HeroTitle, &b.HeroHighlight, &b.HeroDescription, &b.BackgroundColor, &b.PanelColor, &b.TextColor, &b.MutedColor, &b.PrimaryColor, &b.AccentColor, &b.LogoMIME, &b.LogoData)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, err
	}
	return b, d.UpdateBranding(ctx, b)
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func nullBytes(v []byte) any {
	if len(v) == 0 {
		return nil
	}
	return v
}

type Principal struct {
	User    *User
	ShareID string
}

type Artifact struct {
	ID, OwnerID, Slug, Title, Visibility string
	LatestVersion                        int
	CreatedAt, UpdatedAt                 time.Time
	Role                                 string
}

type AdminArtifact struct {
	Artifact
	OwnerEmail, OwnerDisplayName string
	Shares                       []Share
}

type Version struct {
	ArtifactID, Title, Entrypoint, ObjectDir, ManifestSHA256 string
	Version, FileCount                                       int
	ByteSize                                                 int64
	Manifest                                                 json.RawMessage
	CreatedBy                                                string
	CreatedAt                                                time.Time
}

type Upload struct {
	ID, ArtifactID, CreatedBy, Title, Entrypoint string
	TokenHash                                    []byte
	StagedManifest                               json.RawMessage
	StagedSHA256                                 string
	StagedBytes                                  int64
	ExpiresAt                                    time.Time
	CommittedAt                                  *time.Time
	CommittedVersion                             *int
}

type Share struct {
	ID, ArtifactID, CreatedBy string
	Version                   *int
	ExpiresAt                 *time.Time
	ViewLimit                 *int
	ViewCount                 int
	RevokedAt                 *time.Time
}

func (d *DB) ListShares(ctx context.Context, artifactID, userID string) ([]Share, error) {
	rows, err := d.Pool.Query(ctx, `SELECT s.id,s.artifact_id,s.version,s.created_by,s.expires_at,s.view_limit,s.view_count,s.revoked_at
		FROM shares s JOIN artifacts a ON a.id=s.artifact_id
		WHERE s.artifact_id=$1 AND a.owner_id=$2 AND a.deleted_at IS NULL AND s.revoked_at IS NULL
		ORDER BY s.created_at DESC`, artifactID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Share
	for rows.Next() {
		var s Share
		if err := rows.Scan(&s.ID, &s.ArtifactID, &s.Version, &s.CreatedBy, &s.ExpiresAt, &s.ViewLimit, &s.ViewCount, &s.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func Open(ctx context.Context, databaseURL string) (*DB, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	db := &DB{Pool: pool}
	if err := db.Pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) Close() { d.Pool.Close() }

func (d *DB) Migrate(ctx context.Context) error {
	_, err := d.Pool.Exec(ctx, schemaSQL)
	return err
}

func (d *DB) Ping(ctx context.Context) error { return d.Pool.Ping(ctx) }

func (d *DB) CleanupExpired(ctx context.Context, auditBefore time.Time) (_ CleanupResult, _ bool, err error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return CleanupResult{}, false, err
	}
	defer rollbackTransaction(ctx, tx, &err)
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(747823274684676745)`).Scan(&locked); err != nil || !locked {
		return CleanupResult{}, locked, err
	}
	var out CleanupResult
	rows, err := tx.Query(ctx, `DELETE FROM upload_sessions WHERE expires_at<=now() RETURNING id`)
	if err != nil {
		return CleanupResult{}, true, err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return CleanupResult{}, true, err
		}
		out.UploadIDs = append(out.UploadIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return CleanupResult{}, true, err
	}
	out.Uploads = int64(len(out.UploadIDs))
	deleteRows := func(query string, args ...any) (int64, error) {
		tag, err := tx.Exec(ctx, query, args...)
		return tag.RowsAffected(), err
	}
	if out.Sessions, err = deleteRows(`DELETE FROM sessions WHERE expires_at<=now()`); err != nil {
		return CleanupResult{}, true, err
	}
	if out.ShareSessions, err = deleteRows(`DELETE FROM share_sessions WHERE expires_at<=now()`); err != nil {
		return CleanupResult{}, true, err
	}
	if out.OIDCStates, err = deleteRows(`DELETE FROM oidc_states WHERE expires_at<=now()`); err != nil {
		return CleanupResult{}, true, err
	}
	if out.Audits, err = deleteRows(`DELETE FROM audit_events WHERE created_at<$1`, auditBefore); err != nil {
		return CleanupResult{}, true, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CleanupResult{}, true, err
	}
	return out, true, nil
}

func (d *DB) UpsertUser(ctx context.Context, subject, username, email, displayName string, admin bool) (User, error) {
	var u User
	err := d.Pool.QueryRow(ctx, `
		INSERT INTO users (id, oidc_subject, username, email, display_name, is_admin)
		VALUES ('u_' || gen_random_uuid()::text, $1, $2, $3, $4, $5)
		ON CONFLICT (oidc_subject) DO UPDATE SET username=EXCLUDED.username, email=EXCLUDED.email,
			display_name=EXCLUDED.display_name, is_admin=EXCLUDED.is_admin, last_login_at=now()
		RETURNING id, oidc_subject, username, email, display_name, is_admin`,
		subject, username, email, displayName, admin).Scan(&u.ID, &u.Subject, &u.Username, &u.Email, &u.DisplayName, &u.Admin)
	return u, err
}

func (d *DB) CreateSession(ctx context.Context, tokenHash []byte, userID string, expires time.Time) error {
	_, err := d.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,$3)`, tokenHash, userID, expires)
	return err
}

func (d *DB) UserBySession(ctx context.Context, tokenHash []byte) (User, error) {
	var u User
	err := d.Pool.QueryRow(ctx, `SELECT u.id,u.oidc_subject,u.username,u.email,u.display_name,u.is_admin
		FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now()`, tokenHash).
		Scan(&u.ID, &u.Subject, &u.Username, &u.Email, &u.DisplayName, &u.Admin)
	return u, translate(err)
}

func (d *DB) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, tokenHash)
	return err
}

func (d *DB) CreateOIDCState(ctx context.Context, hash []byte, verifier, nonce, returnPath string, expires time.Time) error {
	_, err := d.Pool.Exec(ctx, `INSERT INTO oidc_states(token_hash,pkce_verifier,nonce,return_path,expires_at) VALUES($1,$2,$3,$4,$5)`, hash, verifier, nonce, returnPath, expires)
	return err
}

func (d *DB) ConsumeOIDCState(ctx context.Context, hash []byte) (verifier, nonce, returnPath string, err error) {
	err = d.Pool.QueryRow(ctx, `DELETE FROM oidc_states WHERE token_hash=$1 AND expires_at>now() RETURNING pkce_verifier,nonce,return_path`, hash).Scan(&verifier, &nonce, &returnPath)
	return verifier, nonce, returnPath, translate(err)
}

func (d *DB) ListArtifacts(ctx context.Context, userID string) ([]Artifact, error) {
	rows, err := d.Pool.Query(ctx, `SELECT a.id,a.owner_id,a.slug,a.title,a.visibility,a.latest_version,a.created_at,a.updated_at,
		CASE WHEN a.owner_id=$1 THEN 'owner' ELSE COALESCE(g.role,'viewer') END
		FROM artifacts a LEFT JOIN artifact_grants g ON g.artifact_id=a.id AND g.user_id=$1
		WHERE a.deleted_at IS NULL AND (a.owner_id=$1 OR g.user_id IS NOT NULL OR a.visibility='authenticated') ORDER BY a.updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.OwnerID, &a.Slug, &a.Title, &a.Visibility, &a.LatestVersion, &a.CreatedAt, &a.UpdatedAt, &a.Role); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) ArtifactForUser(ctx context.Context, artifactID, userID string) (Artifact, error) {
	var a Artifact
	err := d.Pool.QueryRow(ctx, `SELECT a.id,a.owner_id,a.slug,a.title,a.visibility,a.latest_version,a.created_at,a.updated_at,
		CASE WHEN a.owner_id=$2 THEN 'owner' ELSE COALESCE(g.role,'viewer') END FROM artifacts a
		LEFT JOIN artifact_grants g ON g.artifact_id=a.id AND g.user_id=$2
		WHERE a.id=$1 AND a.deleted_at IS NULL AND (a.owner_id=$2 OR g.user_id IS NOT NULL OR a.visibility='authenticated')`, artifactID, userID).
		Scan(&a.ID, &a.OwnerID, &a.Slug, &a.Title, &a.Visibility, &a.LatestVersion, &a.CreatedAt, &a.UpdatedAt, &a.Role)
	return a, translate(err)
}

func (d *DB) SetArtifactVisibility(ctx context.Context, artifactID, userID, visibility string) (Artifact, error) {
	if visibility != "private" && visibility != "authenticated" {
		return Artifact{}, ErrInvalidVisibility
	}
	ct, err := d.Pool.Exec(ctx, `UPDATE artifacts SET visibility=$3,updated_at=now()
		WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL`, artifactID, userID, visibility)
	if err != nil {
		return Artifact{}, err
	}
	if ct.RowsAffected() != 1 {
		return Artifact{}, ErrNotFound
	}
	return d.ArtifactForUser(ctx, artifactID, userID)
}

func (d *DB) ListAdminArtifacts(ctx context.Context) ([]AdminArtifact, error) {
	rows, err := d.Pool.Query(ctx, `SELECT a.id,a.owner_id,a.slug,a.title,a.visibility,a.latest_version,a.created_at,a.updated_at,
		u.email,u.display_name FROM artifacts a JOIN users u ON u.id=a.owner_id
		WHERE a.deleted_at IS NULL ORDER BY a.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminArtifact
	byID := make(map[string]int)
	for rows.Next() {
		var a AdminArtifact
		if err := rows.Scan(&a.ID, &a.OwnerID, &a.Slug, &a.Title, &a.Visibility, &a.LatestVersion, &a.CreatedAt, &a.UpdatedAt, &a.OwnerEmail, &a.OwnerDisplayName); err != nil {
			return nil, err
		}
		a.Role = "admin"
		byID[a.ID] = len(out)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	shareRows, err := d.Pool.Query(ctx, `SELECT id,artifact_id,version,created_by,expires_at,view_limit,view_count,revoked_at
		FROM shares WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at>now()) ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer shareRows.Close()
	for shareRows.Next() {
		var share Share
		if err := shareRows.Scan(&share.ID, &share.ArtifactID, &share.Version, &share.CreatedBy, &share.ExpiresAt, &share.ViewLimit, &share.ViewCount, &share.RevokedAt); err != nil {
			return nil, err
		}
		if index, ok := byID[share.ArtifactID]; ok {
			out[index].Shares = append(out[index].Shares, share)
		}
	}
	return out, shareRows.Err()
}

func (d *DB) AdminRevokeShare(ctx context.Context, shareID string) (_ string, err error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackTransaction(ctx, tx, &err)
	var artifactID string
	err = tx.QueryRow(ctx, `UPDATE shares SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL RETURNING artifact_id`, shareID).Scan(&artifactID)
	if err != nil {
		return "", translate(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM share_sessions WHERE share_id=$1`, shareID); err != nil {
		return "", err
	}
	return artifactID, tx.Commit(ctx)
}

func (d *DB) AdminDeleteArtifact(ctx context.Context, artifactID string) (_ AdminArtifact, err error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return AdminArtifact{}, err
	}
	defer rollbackTransaction(ctx, tx, &err)
	var artifact AdminArtifact
	err = tx.QueryRow(ctx, `UPDATE artifacts a SET deleted_at=now(),updated_at=now() FROM users u
		WHERE a.id=$1 AND a.deleted_at IS NULL AND u.id=a.owner_id
		RETURNING a.id,a.owner_id,a.slug,a.title,a.visibility,a.latest_version,a.created_at,a.updated_at,u.email,u.display_name`, artifactID).
		Scan(&artifact.ID, &artifact.OwnerID, &artifact.Slug, &artifact.Title, &artifact.Visibility, &artifact.LatestVersion, &artifact.CreatedAt, &artifact.UpdatedAt, &artifact.OwnerEmail, &artifact.OwnerDisplayName)
	if err != nil {
		return AdminArtifact{}, translate(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM share_sessions WHERE share_id IN (SELECT id FROM shares WHERE artifact_id=$1)`, artifactID); err != nil {
		return AdminArtifact{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE shares SET revoked_at=COALESCE(revoked_at,now()) WHERE artifact_id=$1`, artifactID); err != nil {
		return AdminArtifact{}, err
	}
	return artifact, tx.Commit(ctx)
}

type transactionRollbacker interface {
	Rollback(context.Context) error
}

func rollbackTransaction(ctx context.Context, tx transactionRollbacker, errp *error) {
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		*errp = errors.Join(*errp, err)
	}
}

func (d *DB) CreateUpload(ctx context.Context, u Upload) error {
	_, err := d.Pool.Exec(ctx, `INSERT INTO upload_sessions(id,artifact_id,created_by,title,entrypoint,token_hash,expires_at)
		VALUES($1,NULLIF($2,''),$3,$4,$5,$6,$7)`, u.ID, u.ArtifactID, u.CreatedBy, u.Title, u.Entrypoint, u.TokenHash, u.ExpiresAt)
	return err
}

func (d *DB) UploadByToken(ctx context.Context, id string, hash []byte) (Upload, error) {
	var u Upload
	err := d.Pool.QueryRow(ctx, `SELECT id,COALESCE(artifact_id,''),created_by,title,entrypoint,token_hash,
		COALESCE(staged_manifest,'null'::jsonb),COALESCE(staged_sha256,''),COALESCE(staged_bytes,0),expires_at,committed_at,committed_version
		FROM upload_sessions WHERE id=$1 AND token_hash=$2 AND expires_at>now()`, id, hash).
		Scan(&u.ID, &u.ArtifactID, &u.CreatedBy, &u.Title, &u.Entrypoint, &u.TokenHash, &u.StagedManifest, &u.StagedSHA256, &u.StagedBytes, &u.ExpiresAt, &u.CommittedAt, &u.CommittedVersion)
	return u, translate(err)
}

func (d *DB) MarkUploadStaged(ctx context.Context, id string, hash []byte, manifest json.RawMessage, digest string, bytes int64) error {
	ct, err := d.Pool.Exec(ctx, `UPDATE upload_sessions SET staged_manifest=$3,staged_sha256=$4,staged_bytes=$5
		WHERE id=$1 AND token_hash=$2 AND expires_at>now() AND committed_at IS NULL`, id, hash, manifest, digest, bytes)
	if err == nil && ct.RowsAffected() != 1 {
		return ErrNotFound
	}
	return err
}

type CommitInput struct {
	UploadID, UserID, ArtifactID, Title, Entrypoint, ObjectDir, ManifestSHA256 string
	Manifest                                                                   json.RawMessage
	ByteSize                                                                   int64
	FileCount                                                                  int
}

func (d *DB) CommitVersion(ctx context.Context, in CommitInput) (_ Artifact, _ Version, err error) {
	tx, err := d.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Artifact{}, Version{}, err
	}
	defer rollbackTransaction(ctx, tx, &err)
	artifactID := in.ArtifactID
	if artifactID == "" {
		artifactID = "a_" + in.UploadID
		_, err = tx.Exec(ctx, `INSERT INTO artifacts(id,owner_id,title) VALUES($1,$2,$3)`, artifactID, in.UserID, in.Title)
	} else {
		var owner string
		err = tx.QueryRow(ctx, `SELECT owner_id FROM artifacts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, artifactID).Scan(&owner)
		if err == nil && owner != in.UserID {
			err = errors.New("only the owner may publish versions")
		}
	}
	if err != nil {
		return Artifact{}, Version{}, translate(err)
	}
	var next int
	if err = tx.QueryRow(ctx, `SELECT latest_version+1 FROM artifacts WHERE id=$1 FOR UPDATE`, artifactID).Scan(&next); err != nil {
		return Artifact{}, Version{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO artifact_versions(artifact_id,version,title,entrypoint,object_dir,manifest,manifest_sha256,byte_size,file_count,created_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, artifactID, next, in.Title, in.Entrypoint, in.ObjectDir, in.Manifest, in.ManifestSHA256, in.ByteSize, in.FileCount, in.UserID)
	if err != nil {
		return Artifact{}, Version{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE artifacts SET title=$2,latest_version=$3,updated_at=now() WHERE id=$1`, artifactID, in.Title, next)
	if err != nil {
		return Artifact{}, Version{}, err
	}
	ct, err := tx.Exec(ctx, `UPDATE upload_sessions SET artifact_id=$2,committed_at=now(),committed_version=$4 WHERE id=$1 AND created_by=$3 AND committed_at IS NULL`, in.UploadID, artifactID, in.UserID, next)
	if err != nil || ct.RowsAffected() != 1 {
		return Artifact{}, Version{}, errors.New("upload cannot be committed")
	}
	if err = tx.Commit(ctx); err != nil {
		return Artifact{}, Version{}, err
	}
	a, err := d.ArtifactForUser(ctx, artifactID, in.UserID)
	if err != nil {
		return Artifact{}, Version{}, err
	}
	v, err := d.Version(ctx, artifactID, next)
	return a, v, err
}

func (d *DB) Version(ctx context.Context, artifactID string, version int) (Version, error) {
	var v Version
	err := d.Pool.QueryRow(ctx, `SELECT v.artifact_id,v.version,v.title,v.entrypoint,v.object_dir,v.manifest,v.manifest_sha256,v.byte_size,v.file_count,v.created_by,v.created_at
		FROM artifact_versions v JOIN artifacts a ON a.id=v.artifact_id
		WHERE v.artifact_id=$1 AND v.version=$2 AND a.deleted_at IS NULL`, artifactID, version).
		Scan(&v.ArtifactID, &v.Version, &v.Title, &v.Entrypoint, &v.ObjectDir, &v.Manifest, &v.ManifestSHA256, &v.ByteSize, &v.FileCount, &v.CreatedBy, &v.CreatedAt)
	return v, translate(err)
}

func (d *DB) VersionForShare(ctx context.Context, shareID string) (Version, error) {
	var artifactID string
	var version int
	err := d.Pool.QueryRow(ctx, `SELECT s.artifact_id,COALESCE(s.version,a.latest_version) FROM shares s JOIN artifacts a ON a.id=s.artifact_id
		WHERE s.id=$1 AND s.revoked_at IS NULL AND (s.expires_at IS NULL OR s.expires_at>now())`, shareID).Scan(&artifactID, &version)
	if err != nil {
		return Version{}, translate(err)
	}
	return d.Version(ctx, artifactID, version)
}

func (d *DB) CreateShare(ctx context.Context, s Share, tokenHash []byte) error {
	ct, err := d.Pool.Exec(ctx, `INSERT INTO shares(id,artifact_id,version,created_by,token_hash,expires_at,view_limit)
		SELECT $1,$2,$3,$4,$5,$6,$7 FROM artifacts a WHERE a.id=$2 AND a.owner_id=$4 AND a.deleted_at IS NULL
		AND ($3::integer IS NULL OR EXISTS (SELECT 1 FROM artifact_versions v WHERE v.artifact_id=a.id AND v.version=$3))`, s.ID, s.ArtifactID, s.Version, s.CreatedBy, tokenHash, s.ExpiresAt, s.ViewLimit)
	if err == nil && ct.RowsAffected() != 1 {
		return ErrNotFound
	}
	return err
}

func (d *DB) ExchangeShare(ctx context.Context, tokenHash, sessionHash []byte, expires time.Time) (_ Share, err error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return Share{}, err
	}
	defer rollbackTransaction(ctx, tx, &err)
	var s Share
	err = tx.QueryRow(ctx, `UPDATE shares SET view_count=view_count+1 WHERE token_hash=$1 AND revoked_at IS NULL
		AND (expires_at IS NULL OR expires_at>now()) AND (view_limit IS NULL OR view_count<view_limit)
		RETURNING id,artifact_id,version,created_by,expires_at,view_limit,view_count,revoked_at`, tokenHash).
		Scan(&s.ID, &s.ArtifactID, &s.Version, &s.CreatedBy, &s.ExpiresAt, &s.ViewLimit, &s.ViewCount, &s.RevokedAt)
	if err != nil {
		return Share{}, translate(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO share_sessions(token_hash,share_id,expires_at) VALUES($1,$2,$3)`, sessionHash, s.ID, expires); err != nil {
		return Share{}, err
	}
	return s, tx.Commit(ctx)
}

func (d *DB) ShareBySession(ctx context.Context, sessionHash []byte) (Share, error) {
	var s Share
	err := d.Pool.QueryRow(ctx, `SELECT s.id,s.artifact_id,s.version,s.created_by,s.expires_at,s.view_limit,s.view_count,s.revoked_at
		FROM share_sessions ss JOIN shares s ON s.id=ss.share_id WHERE ss.token_hash=$1 AND ss.expires_at>now()
		AND s.revoked_at IS NULL AND (s.expires_at IS NULL OR s.expires_at>now())`, sessionHash).
		Scan(&s.ID, &s.ArtifactID, &s.Version, &s.CreatedBy, &s.ExpiresAt, &s.ViewLimit, &s.ViewCount, &s.RevokedAt)
	return s, translate(err)
}

func (d *DB) RevokeShare(ctx context.Context, shareID, userID string) (string, error) {
	var artifactID string
	err := d.Pool.QueryRow(ctx, `UPDATE shares s SET revoked_at=now() FROM artifacts a
		WHERE s.id=$1 AND a.id=s.artifact_id AND a.owner_id=$2 AND s.revoked_at IS NULL
		RETURNING s.artifact_id`, shareID, userID).Scan(&artifactID)
	return artifactID, translate(err)
}

func (d *DB) Audit(ctx context.Context, userID, shareID, artifactID, action, requestID string, remote netip.Addr, details any) error {
	b, err := json.Marshal(details)
	if err != nil {
		return err
	}
	var user, share, artifact any
	if userID != "" {
		user = userID
	}
	if shareID != "" {
		share = shareID
	}
	if artifactID != "" {
		artifact = artifactID
	}
	var ip any
	if remote.IsValid() {
		ip = remote.String()
	}
	_, err = d.Pool.Exec(ctx, `INSERT INTO audit_events(actor_user_id,actor_share_id,artifact_id,action,request_id,remote_ip,details)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, user, share, artifact, action, requestID, ip, b)
	return err
}

func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
