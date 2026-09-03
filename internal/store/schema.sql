CREATE TABLE IF NOT EXISTS schema_version (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    version integer NOT NULL
);
INSERT INTO schema_version (singleton, version) VALUES (true, 1)
ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE IF NOT EXISTS users (
    id text PRIMARY KEY,
    oidc_subject text NOT NULL UNIQUE,
    username text NOT NULL,
    email text NOT NULL DEFAULT '',
    display_name text NOT NULL DEFAULT '',
    is_admin boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash bytea PRIMARY KEY,
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sessions_expires_idx ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS oidc_states (
    token_hash bytea PRIMARY KEY,
    pkce_verifier text NOT NULL,
    nonce text NOT NULL,
    return_path text NOT NULL DEFAULT '/',
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS oidc_states_expires_idx ON oidc_states(expires_at);

CREATE TABLE IF NOT EXISTS artifacts (
    id text PRIMARY KEY,
    owner_id text NOT NULL REFERENCES users(id),
    slug text NOT NULL DEFAULT '',
    title text NOT NULL,
    visibility text NOT NULL DEFAULT 'private',
    latest_version integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);
ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS visibility text NOT NULL DEFAULT 'private';
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='artifacts_visibility_check' AND conrelid='artifacts'::regclass) THEN
        ALTER TABLE artifacts ADD CONSTRAINT artifacts_visibility_check CHECK (visibility IN ('private', 'authenticated'));
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS artifacts_authenticated_idx ON artifacts(updated_at DESC) WHERE deleted_at IS NULL AND visibility='authenticated';
CREATE INDEX IF NOT EXISTS artifacts_owner_idx ON artifacts(owner_id, updated_at DESC) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS artifact_grants (
    artifact_id text NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('viewer', 'editor')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (artifact_id, user_id)
);

CREATE TABLE IF NOT EXISTS artifact_versions (
    artifact_id text NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    version integer NOT NULL CHECK (version > 0),
    title text NOT NULL,
    entrypoint text NOT NULL,
    object_dir text NOT NULL UNIQUE,
    manifest jsonb NOT NULL,
    manifest_sha256 text NOT NULL,
    byte_size bigint NOT NULL CHECK (byte_size >= 0),
    file_count integer NOT NULL CHECK (file_count > 0),
    created_by text NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (artifact_id, version)
);

CREATE TABLE IF NOT EXISTS upload_sessions (
    id text PRIMARY KEY,
    artifact_id text REFERENCES artifacts(id) ON DELETE CASCADE,
    created_by text NOT NULL REFERENCES users(id),
    title text NOT NULL,
    entrypoint text NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    staged_manifest jsonb,
    staged_sha256 text,
    staged_bytes bigint,
    expires_at timestamptz NOT NULL,
    committed_at timestamptz,
    committed_version integer,
    created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE upload_sessions ADD COLUMN IF NOT EXISTS committed_version integer;
CREATE INDEX IF NOT EXISTS upload_sessions_expires_idx ON upload_sessions(expires_at) WHERE committed_at IS NULL;

CREATE TABLE IF NOT EXISTS shares (
    id text PRIMARY KEY,
    artifact_id text NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    version integer,
    created_by text NOT NULL REFERENCES users(id),
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz,
    view_limit integer CHECK (view_limit IS NULL OR view_limit > 0),
    view_count integer NOT NULL DEFAULT 0,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS shares_artifact_idx ON shares(artifact_id, created_at DESC);
UPDATE shares s SET version=a.latest_version FROM artifacts a
WHERE s.artifact_id=a.id AND s.version IS NULL;
ALTER TABLE shares ALTER COLUMN version SET NOT NULL;

CREATE TABLE IF NOT EXISTS share_sessions (
    token_hash bytea PRIMARY KEY,
    share_id text NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

DROP TABLE IF EXISTS annotations;

CREATE TABLE IF NOT EXISTS audit_events (
    id bigserial PRIMARY KEY,
    actor_user_id text REFERENCES users(id) ON DELETE SET NULL,
    actor_share_id text REFERENCES shares(id) ON DELETE SET NULL,
    artifact_id text REFERENCES artifacts(id) ON DELETE SET NULL,
    action text NOT NULL,
    request_id text NOT NULL,
    remote_ip inet,
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS audit_events_artifact_idx ON audit_events(artifact_id, created_at DESC);
CREATE INDEX IF NOT EXISTS audit_events_created_idx ON audit_events(created_at);

CREATE TABLE IF NOT EXISTS instance_branding (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    site_name text NOT NULL,
    tagline text NOT NULL,
    hero_title text NOT NULL,
    hero_highlight text NOT NULL,
    hero_description text NOT NULL,
    background_color text NOT NULL,
    panel_color text NOT NULL,
    text_color text NOT NULL,
    muted_color text NOT NULL,
    primary_color text NOT NULL,
    accent_color text NOT NULL,
    logo_mime text,
    logo_data bytea,
    favicon_mime text,
    favicon_data bytea,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((logo_mime IS NULL) = (logo_data IS NULL)),
    CHECK ((favicon_mime IS NULL) = (favicon_data IS NULL))
);
ALTER TABLE instance_branding ADD COLUMN IF NOT EXISTS theme_name text NOT NULL DEFAULT 'Default';
ALTER TABLE instance_branding ADD COLUMN IF NOT EXISTS favicon_mime text;
ALTER TABLE instance_branding ADD COLUMN IF NOT EXISTS favicon_data bytea;
INSERT INTO instance_branding (
    singleton,site_name,tagline,hero_title,hero_highlight,hero_description,
    background_color,panel_color,text_color,muted_color,primary_color,accent_color
) VALUES (
    true,'Rendercase','Self-hosted artifacts','Make it tangible.','Keep it yours.',
    'Interactive artifacts published by you and your tools, hosted entirely on your infrastructure.',
    '#03060f','#0a0f1d','#e8f3ff','#8292a8','#2dd4bf','#a78bfa'
) ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE IF NOT EXISTS branding_themes (
    name text PRIMARY KEY,
    tagline text NOT NULL, hero_title text NOT NULL, hero_highlight text NOT NULL, hero_description text NOT NULL,
    site_name text NOT NULL, background_color text NOT NULL, panel_color text NOT NULL, text_color text NOT NULL,
    muted_color text NOT NULL, primary_color text NOT NULL, accent_color text NOT NULL,
    logo_mime text, logo_data bytea, favicon_mime text, favicon_data bytea,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((logo_mime IS NULL) = (logo_data IS NULL)),
    CHECK ((favicon_mime IS NULL) = (favicon_data IS NULL))
);
ALTER TABLE branding_themes ADD COLUMN IF NOT EXISTS favicon_mime text;
ALTER TABLE branding_themes ADD COLUMN IF NOT EXISTS favicon_data bytea;
INSERT INTO branding_themes (name,site_name,tagline,hero_title,hero_highlight,hero_description,background_color,panel_color,text_color,muted_color,primary_color,accent_color,logo_mime,logo_data,favicon_mime,favicon_data)
SELECT theme_name,site_name,tagline,hero_title,hero_highlight,hero_description,background_color,panel_color,text_color,muted_color,primary_color,accent_color,logo_mime,logo_data,favicon_mime,favicon_data FROM instance_branding WHERE singleton=true
ON CONFLICT (name) DO NOTHING;
