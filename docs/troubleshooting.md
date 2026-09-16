# Troubleshooting

## The process does not start

Read the first configuration error in the Rendercase log:

```sh
docker compose ps
docker compose logs --since=15m rendercase postgres
docker compose --env-file .env config --quiet
```

The management and content URLs must use different hostnames and HTTPS outside
loopback. `RENDERCASE_COOKIE_SECRET` must decode from base64 to at least 32
bytes. Confirm the database URL, OIDC issuer, OAuth audience, and selected
browser-auth mode before investigating the proxy.

## Health works but readiness fails

`/healthz` only proves that the process answers HTTP. `/readyz` also checks
PostgreSQL and artifact storage. Inspect the log for the named dependency,
then verify database health, filesystem permissions and free space, or S3
credentials, bucket, region, endpoint, and prefix.

Call readiness on the management hostname. The content hostname intentionally
serves only `/healthz` and ticketed artifact files.

## Requests return `421 Misdirected Request`

The incoming `Host` does not match either configured URL. Preserve the original
host through the reverse proxy and route both names to Rendercase. Do not
rewrite both routes to one host or configure the same hostname for management
and untrusted content.

## Browser login loops or fails

For OIDC mode, compare the registered callback exactly with
`RENDERCASE_OIDC_REDIRECT_URL`, including scheme and hostname. Check that the
issuer is reachable from the container and that the browser can retain secure
cookies. Rotating the cookie secret intentionally signs everyone out.

For Cloudflare Access mode, confirm the team domain, application audience, and
the `Cf-Access-Jwt-Assertion` reaching the origin. Rendercase verifies that
assertion cryptographically. A Cloudflare `401` or `403` generated before the
origin is an Access policy or service-token problem, not an application role
problem.

Keep the management hostname protected. Configure only the narrowly documented
bypasses for capability/static paths and the `PUT /upload/*` capability route;
do not bypass `/api/v1/uploads/*`.

## The MCP client receives `401` or cannot initialize

Use the management origin's `/mcp` endpoint. The access token must contain the
configured audience and `rendercase:mcp` scope, and its issuer must match the
configured OIDC provider. Check
`/.well-known/oauth-protected-resource/mcp` from the client network when OAuth
discovery fails.

In Cloudflare Access mode, the edge may authenticate the OAuth or service token
and inject its verified assertion after removing `Authorization`; make sure the
Access application and route support that client flow.

Switchboard delegation works only when the bearer belongs to the exact
`RENDERCASE_SWITCHBOARD_OAUTH_SUBJECT`. Other callers cannot make
`X-Switchboard-OAuth-Subject` authoritative, and an unknown delegated subject
fails closed. Confirm the target user has signed in to Rendercase at least once
and restrict the upstream route to the gateway network.

## An upload or commit fails

Upload sessions expire after `RENDERCASE_UPLOAD_TTL` and cannot be revived;
create a new upload. Send the capability in `X-Rendercase-Upload-Token` or the
documented authorization header, never in a query string. Commit through the
authenticated nested endpoint only after the ZIP upload succeeds.

ZIP validation rejects path traversal, symlinks, duplicate paths, missing
entrypoints, too many files, and bundles whose archive or expanded content
exceeds the configured limit. Keep `index.html` at the declared entrypoint and
inspect the JSON error before raising limits.

## The library works but an artifact does not render

Confirm that the content hostname is reachable without the management
Cloudflare Access application and that the proxy preserves its `Host`. The
viewer obtains a short-lived signed content ticket; expired tickets require a
page reload. Capability links first exchange their token for a secure share
cookie, so blocked cookies or an expired/revoked share also prevent rendering.

If the artifact loads but scripts, styles, or remote resources do not, inspect
the browser console. Rendercase applies a restrictive Content Security Policy
and sandbox; build the artifact as a self-contained bundle compatible with
that boundary instead of weakening the management origin.

## Safe restart and recovery

Restarting Rendercase interrupts in-flight uploads but does not mutate committed
artifact versions. After a restart, require `/readyz`, reconnect MCP clients,
and retry an uncommitted upload with a new session if its capability expired.

Do not delete object files to repair metadata, and do not edit authorization
rows while traffic is active. Preserve PostgreSQL and artifact storage
together, take a fresh backup, and follow the matched restore procedure in
[Operations and recovery](operations.md).
