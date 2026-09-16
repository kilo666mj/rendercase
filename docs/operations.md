# Operations and recovery

## Routine checks

Check Rendercase through both public routes so the reverse proxy and host
routing are part of the test:

```sh
curl -fsS -o /dev/null https://rendercase.example.com/healthz
curl -fsS -o /dev/null https://rendercase.example.com/readyz
curl -fsS -o /dev/null https://content.rendercase.example.com/healthz
docker compose ps
docker compose logs --since=30m rendercase postgres
```

`/healthz` returns `204` when the process can answer HTTP. The management
origin's `/readyz` returns `204` only when PostgreSQL and the configured object
store are usable. A `421` normally means the proxy sent an unexpected `Host`.

Monitor database connections, storage capacity, reverse-proxy errors, OIDC
availability, and upload rejection rates. In filesystem mode, monitor both the
artifact volume and upload staging space. In S3 mode, the local storage root is
still bounded staging space and must not fill.

## Upgrade

1. Record the running revision and read the release notes.
2. Back up PostgreSQL and artifact storage as one recovery point, plus `.env`.
3. Validate the candidate source:

   ```sh
   go test ./...
   go vet ./...
   go build ./cmd/rendercase ./cmd/rendercase-cli ./cmd/rendercase-mtls-proxy
   docker compose --env-file .env config --quiet
   ```

4. Rebuild and recreate the application:

   ```sh
   docker compose up -d --build rendercase
   ```

5. Require the management health and readiness checks and the content health
   check to pass. Sign in, open an existing artifact, and publish a disposable
   bundle through the same client path used in production.

Rendercase applies its PostgreSQL schema changes during application startup.
Do not edit `schema_version` or mark a schema change complete by hand. Roll back
to the recorded application revision only with a database and object-store
state that it supports.

## Back up

PostgreSQL contains identities, authorization, artifact/version metadata,
shares, sessions, and audit records. Artifact storage contains the immutable
bundle objects referenced by that metadata. A useful recovery point needs
both; a database-only restore can refer to missing objects, while an
object-only restore has no authorization or version index.

Create a logical database backup:

```sh
docker compose exec -T postgres \
  pg_dump -U rendercase -d rendercase -Fc > rendercase.dump
```

For filesystem storage, snapshot or back up the `artifact-data` volume at the
same recovery point. For S3 storage, use bucket versioning or a coordinated
bucket backup for the configured prefix. Rendercase does not delete published
objects during normal operation, but that does not replace a backup.

Back up `.env` separately as sensitive configuration. It contains secrets and
the exact public/content origins, OIDC resource settings, storage backend, and
delegation boundary needed to interpret the database correctly.

## Restore

Restore PostgreSQL and artifact objects from the same recovery point:

```sh
docker compose stop rendercase
docker compose exec -T postgres \
  pg_restore --clean --if-exists -U rendercase -d rendercase < rendercase.dump
docker compose up -d rendercase
```

Restore the filesystem volume or S3 prefix before admitting traffic. Then
verify both hosts, `/readyz`, sign-in, a private artifact, an authenticated
artifact if used, and a capability share. Publish a disposable artifact and
remove access to it after the test.

Administrative deletion is soft deletion: it hides the artifact and revokes
shares but deliberately leaves immutable objects in storage. There is no public
undelete endpoint. Recover an accidentally deleted artifact from a tested
database recovery point or use an operator-reviewed database repair only after
taking another backup and confirming the matching objects still exist.

## Rotate credentials

- Changing `RENDERCASE_COOKIE_SECRET` invalidates existing browser and share
  session cookies. Schedule it as a sign-in interruption.
- Rotate the OIDC client secret in the provider and Rendercase together, then
  verify browser login and logout.
- Rotate S3 credentials or workload identity without changing the bucket or
  prefix, and require `/readyz` plus a publish/read test.
- When changing `RENDERCASE_SWITCHBOARD_OAUTH_SUBJECT`, update Switchboard's
  service credential and Rendercase as one change. The old subject must no
  longer be able to delegate.
- Revoke capability shares through the owner or administrator API; do not try
  to recover their plaintext tokens from PostgreSQL because only hashes are
  stored.
