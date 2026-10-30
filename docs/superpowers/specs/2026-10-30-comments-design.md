# Artifact comments design

Date: 2026-10-30
Status: Draft for review

## Goal

Signed-in reviewers can leave comments on specific parts of a rendered
artifact and reply to existing comments, forming threads. Agents that publish
artifacts can read that feedback, reply, and resolve threads over MCP, closing
the review loop: publish → review → fix → resolve.

This replaces the idea behind the `annotations` table dropped from the initial
release; nothing of its shape is reused.

### Decisions already made

- Only signed-in users who can view the artifact may read or write comments.
  Capability share links (`/s/*`, `/shared/*`) never see comments.
- Comments anchor to elements or text inside the artifact through a small
  bridge script served by the content origin.
- MCP gets read, reply, and resolve. Agents cannot open new threads.

### Non-goals

- Anonymous or share-link commenting.
- Rich text, Markdown rendering, attachments, mentions, or notifications.
- Agent-created anchored threads.
- Live updates (polling or push). The sidebar refreshes on open and after the
  viewer's own mutations.

## Data model

Added to `internal/store/schema.sql` as idempotent DDL, matching the existing
style.

```sql
CREATE TABLE IF NOT EXISTS comment_threads (
    id text PRIMARY KEY,
    artifact_id text NOT NULL,
    version integer NOT NULL,
    anchor jsonb,
    created_by text NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    resolved_by text REFERENCES users(id),
    FOREIGN KEY (artifact_id, version) REFERENCES artifact_versions(artifact_id, version) ON DELETE CASCADE,
    CHECK ((resolved_at IS NULL) = (resolved_by IS NULL))
);
CREATE INDEX IF NOT EXISTS comment_threads_artifact_idx ON comment_threads(artifact_id, version, created_at);

CREATE TABLE IF NOT EXISTS comments (
    id text PRIMARY KEY,
    thread_id text NOT NULL REFERENCES comment_threads(id) ON DELETE CASCADE,
    author_id text NOT NULL REFERENCES users(id),
    body text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    edited_at timestamptz,
    deleted_at timestamptz
);
CREATE INDEX IF NOT EXISTS comments_thread_idx ON comments(thread_id, created_at);
```

- `anchor` is null for a general comment on the version.
- A thread's first comment is its root. Replies are flat and ordered by
  `created_at`; there is no nesting below a thread.
- Deletion is soft: `deleted_at` is set and `body` cleared to `''`. Deleted
  comments are returned as placeholders so thread structure remains readable.
  A thread whose comments are all deleted is hidden from listings.
- Soft-deleted artifacts hide their threads because every query joins
  `artifacts` with `deleted_at IS NULL`, as existing queries do.

### Anchor format

```json
{
  "selector": "main > section:nth-of-type(2) > p:nth-of-type(1)",
  "page": "guide/setup.html",
  "quote": {"exact": "selected text", "prefix": "up to 32 chars", "suffix": "up to 32 chars"},
  "point": {"x": 0.42, "y": 0.18}
}
```

- `selector`: at most 512 bytes, built by the bridge from the target element up
  to the nearest element with a unique `id`, or to `body`.
- `page`: optional. The bundle file the anchor was picked on: the frame URL
  path after `/t/{ticket}/{artifact}/{version}/`, URL-decoded. At most 512
  bytes of printable text. Every HTML file in a bundle gets the bridge, so the
  bridge only places pins whose `page` is absent or equals its own page; pins
  for other pages are neither shown nor reported unanchored. The sidebar shows
  the page name as a tag.
- `quote`: optional. `exact` at most 500 characters, `prefix` and `suffix` at
  most 32 each. Present when the reviewer selected text; otherwise the
  element's leading text (trimmed, at most 120 characters) for verification.
- `point`: the click position as fractions (0–1) of the target element's
  bounding box, used to place the pin.
- The server validates types, lengths, and ranges, and rejects unknown keys.
  Anchors are stored as untrusted hints and never interpreted server-side.

### Limits

- Comment body: 1–10,000 characters after trimming, plain text.
- At most 2,000 comments (including deleted) per artifact; further creates
  return `409`.
- Request bodies for comment endpoints are capped at 64 KiB.

## Permissions

Access is derived from the existing `ArtifactForUser` lookup and its `Role`.

| Action | Allowed |
| --- | --- |
| List threads, create thread, reply | Any user `ArtifactForUser` resolves (owner, grant, or `authenticated` visibility) |
| Edit own comment | The comment author |
| Delete a comment | The author, the owner, or an editor |
| Resolve or reopen a thread | The thread author, the owner, or an editor |

- Users without access receive `404`, as for other artifact routes.
- Administrators do not gain comment access; they continue not to inherit
  owner/editor rights.
- Share sessions and the `/shared/*` viewer have no comment routes, no
  comment UI, and no bridge.
- A delegated Switchboard user is recorded as the author, as for publishing.

Every mutation writes an audit event with ids only, never comment bodies:
`comment.thread.create`, `comment.create`, `comment.edit`, `comment.delete`,
`comment.thread.resolve`, `comment.thread.reopen`.

## Content bridge

### Delivery

- `contentMux` gains `GET /_rc/bridge.js`, served from an embedded file. The
  path is outside the `/t/{ticket}/...` bundle namespace, so it cannot collide
  with uploaded files. The content CSP already permits `script-src 'self'`.
- `renderViewer` mints tickets with the subject `artifact:version:c` when the
  viewer is a signed-in user, and `artifact:version` for share views. The
  content handler accepts both forms and injects the bridge only for `:c`.
- Injection applies when the served file's type is `text/html` and its size is
  at most 5 MiB. The handler buffers the file and inserts
  `<script src="/_rc/bridge.js"></script>` immediately after the opening
  `<head ...>` tag. If there is no `<head>`, the script goes after the
  `<html ...>` tag; if there is no `<html>` either, after the doctype; if there
  is no doctype, at the start of the file. Matching is case-insensitive and
  scans only the first 64 KiB. Tags inside `<!-- ... -->` comments in that
  range are skipped; an unterminated comment hides everything after its
  start, so the search falls through to the next rule.
- Injected responses ignore `Range` and are served whole with a recomputed
  `Content-Length`. All other responses keep the current `http.ServeContent`
  streaming path unchanged, so share views remain byte-identical to the bundle.

### Messaging protocol

All messages are `{rc: 1, type, ...}` objects sent with `postMessage`.

The iframe is sandboxed without `allow-same-origin`, so its origin is opaque
(`"null"`). The parent therefore posts to the frame with target origin `'*'`,
and the bridge learns the management origin from the `enable` message.

Parent → bridge (target origin `'*'`):

- `enable` — start listening; the bridge stays inert until it arrives.
- `pick` / `cancel-pick` — enter or leave pick mode.
- `pins` — `{gen, pins: [{n, anchor, resolved}]}` where `n` is the display
  number and `gen` an integer the parent increments on every `pins` message.
  No comment bodies, author names, or thread ids are sent.
- `focus` — `{n}`: scroll to and highlight that pin.

Bridge → parent (target origin: the origin of the `enable` message, except
`ready`, which carries no data and uses `'*'`):

- `ready` — sent once when the bridge loads.
- `enabled` — reply to `enable`.
- `picked` — `{anchor}` from a click or text selection in pick mode. The
  bridge adds its own `page` to the anchor. The parent ignores `picked`
  unless it started a pick, and opening the composer over an open draft keeps
  the draft and replaces only the anchor.
- `pick-cancelled` — Escape pressed inside the frame while picking.
- `pin-click` — `{n}`.
- `anchors` — `{gen, unanchored: [n, ...]}` after placing pins, echoing the
  `gen` of the last `pins` message. The parent ignores an `anchors` whose `gen`
  is not its current one, and re-renders the sidebar only when the unanchored
  set changes, so focus and open reply or edit forms survive.

### Behaviour

- In pick mode the bridge outlines the element under the pointer, suppresses
  the artifact's click handlers for that one click, and leaves pick mode after
  sending `picked` or on `Escape`.
- Re-anchoring order: query the selector and accept it if the stored quote (or
  leading text) still matches; otherwise search the document text for the
  quote using prefix and suffix to disambiguate; otherwise report the pin as
  unanchored.
- Pins are absolutely positioned elements inside a shadow root attached to a
  bridge-owned host element. Positions update on scroll and resize via
  `requestAnimationFrame`.
- Pins for anchors whose `page` differs from the current page are skipped:
  not drawn and not reported unanchored.
- DOM mutations (other than the bridge re-appending its own host) schedule a
  check at most every 250 ms. It re-appends the host if the artifact removed
  it, retries pins that are unanchored or whose element left the document,
  and sends `anchors` only when the unanchored set changes. This covers
  artifacts that render with script after `DOMContentLoaded`.

### Trust model

- The artifact's own scripts share the bridge's origin and can impersonate it
  or tamper with pins. The design accepts that and limits what they can gain:
  - The parent accepts a message only when `event.source` is the iframe's
    `contentWindow` and `event.origin` is `"null"` (the sandboxed frame) or
    the content origin. It validates
    every field against the anchor limits above and drops anything else.
  - Comment text is only typed into the trusted parent page and only sent
    to the management API by the parent.
  - The iframe never receives comment bodies, author identities, or ids, so
    artifact code cannot read the discussion.
  - A forged `picked` can only pre-fill an anchor the reviewer then sees
    quoted in the composer before submitting.
- The content CSP, iframe `sandbox` attributes, and `connect-src 'none'` are
  unchanged.

## REST API

All routes are on the management origin behind `requireUser`.

| Method and path | Body | Result |
| --- | --- | --- |
| `GET /api/v1/artifacts/{artifact}/threads?version=N\|all&resolved=false\|true\|all` | — | `{threads: [...]}` |
| `POST /api/v1/artifacts/{artifact}/threads` | `{version, anchor?, body}` | `201` thread with root comment |
| `POST /api/v1/threads/{thread}/comments` | `{body}` | `201` comment |
| `PATCH /api/v1/comments/{comment}` | `{body}` | updated comment |
| `DELETE /api/v1/comments/{comment}` | — | `204` |
| `PUT /api/v1/threads/{thread}/resolution` | `{resolved: bool}` | updated thread |

- `version` defaults to the latest version; `resolved` defaults to `false`.
- A thread in the response has `id`, `version`, `anchor`, `created_by`
  (`{id, display_name}`), `created_at`, `resolved_at`, `resolved_by`, and
  `comments`. Each comment has `id`, `author`, `body`, `created_at`,
  `edited_at`, `deleted`, plus `can_edit` and `can_delete` for the caller.
  The thread also carries `can_resolve`. Null fields (`anchor`, `edited_at`,
  `resolved_at`, `resolved_by`) are omitted.
- Creating a thread on a version that does not exist returns `404`.
- Errors use the existing JSON `{error}` shape.

## MCP tools

| Tool | Input | Annotations |
| --- | --- | --- |
| `rendercase_list_comments` | `artifact_id`, `version` (0 = all), `include_resolved` | read-only |
| `rendercase_reply_comment` | `thread_id`, `body` | mutating |
| `rendercase_resolve_comment_thread` | `thread_id`, `resolved` | mutating |

- Output mirrors the REST thread shape. The anchor's `selector` and
  `quote.exact` are included so an agent can locate the element in its source.
- The `rendercase-artifact` skill and README describe the review loop: list
  open comments, publish a new version, reply, and resolve.

## Viewer UI

- `viewer.html` adds a **Comments (N)** button for signed-in viewers, where N
  counts open threads across all versions. It opens a sidebar to the right of
  the iframe; below 760px wide it becomes a bottom sheet.
- New `internal/app/web/comments.js`, served like `viewer.js`. The sharing
  code in `viewer.js` is unchanged.
- **Add comment** sends `pick`. A `picked` message opens the composer showing
  the quoted text. **Comment on whole version** creates a thread with no
  anchor.
- Threads list in the sidebar with number, quote, root comment, reply count,
  and resolved state. Selecting one sends `focus`; a `pin-click` scrolls the
  sidebar to that thread.
- Filters: open or resolved, and this version or all versions. Threads from
  earlier versions show a `vN` tag, and their anchors are included in `pins`
  so they re-attach to the current version when possible. Anchors with a
  `page` show the page name as a tag.
- Unanchored threads list under an **Unanchored** heading.
- All user content is rendered with `textContent`; times use
  `toLocaleString`, as in the sharing dialog.

## Code layout

- `internal/store/comments.go`: types and queries, following
  `publisher_viewers.go`.
- `internal/app/comments.go`: validation, view types, and permission-checked
  operations shared by REST and MCP.
- `internal/app/comments_http.go`: REST handlers.
- `internal/app/bridge.go`: HTML injection helper and the `/_rc/bridge.js`
  route.
- `internal/app/web/bridge.js` and `internal/app/web/comments.js`.
- MCP tools added in `internal/app/mcp.go` next to the existing tools, calling
  the same store and permission functions as the REST handlers.

## Testing

- Store integration tests (`internal/testdb`): create, reply, edit, soft
  delete, resolve and reopen, version FK cascade, soft-deleted artifact hides
  threads, and the per-artifact cap.
- Server tests:
  - a viewer with access can list and create;
  - a user without access gets `404`;
  - a share session gets no comment access;
  - only the author can edit;
  - the author, owner, and editor can delete, and other viewers cannot;
  - the thread author, owner, and editor can resolve;
  - body, anchor, and request-size limits are rejected;
  - audit events contain no comment bodies.
- Content tests:
  - the bridge is injected only with a `:c` ticket;
  - only `text/html` at or under 5 MiB is injected;
  - placement after `<head>`, `<html>`, the doctype, or at the start;
  - `Range` is ignored on injected responses and honoured on others;
  - non-HTML and share-ticket responses are byte-identical to the stored
    object.
- MCP tests for the three tools, including the permission failures above.
- Manual browser check of `bridge.js` and `comments.js` against a running
  instance: pick by click and by selection, pins following scroll and resize,
  re-anchoring after a new version, unanchored fallback, and a forged
  `postMessage` from artifact code being ignored or limited as described. The
  repository has no JavaScript test harness, and this design does not add one.
