---
name: rendercase-artifact
description: Create, update, and publish self-contained HTML artifacts to Rendercase, including architecture diagrams, comparisons, plans, and technical reports. Use when the user asks for a Rendercase artifact or a durable hosted visual deliverable; do not route through another artifact host unless the user explicitly requests it.
---

# Rendercase Artifact

Create the artifact directly and publish it with the Rendercase MCP. Rendercase
is the durable host and version store; local HTML generation is the authoring
step. Do not start a separate artifact-review or hosting workflow unless the
user explicitly asks.

## Workflow

1. Identify whether this is a new artifact or a new immutable version of an
   existing artifact. Use `rendercase_list` or `rendercase_get` when the user
   refers to an existing artifact but has not supplied its ID.
2. Inspect the design system of the project the artifact represents. Reuse its
   colors, typography, spacing, and brand assets when available. Otherwise use
   a restrained, accessible standalone design.
3. Read [references/authoring.md](references/authoring.md) when the artifact
   contains diagrams, comparisons, tables, plans, or other visual structure.
4. Build under a temporary directory, normally with `index.html` as the
   entrypoint. Keep every required local asset inside the bundle and use
   relative paths. Prefer a single self-contained HTML file for modest
   artifacts.
5. Inspect the source content for secrets and unnecessary private operational
   details before packaging. A request to create an artifact authorizes
   publishing the requested content to the user's private Rendercase account,
   but does not authorize creating a capability share.
6. Read [references/publishing.md](references/publishing.md), validate the
   bundle, package it as ZIP, and publish with the Rendercase MCP. Updating an
   artifact requires its `artifact_id`; otherwise create a new artifact.
7. Return the Rendercase artifact URL, title, version, and a short description
   of what it contains. Create a share link only when explicitly requested.

## Invariants

- Produce an actual artifact, not merely HTML pasted into chat.
- Treat Rendercase versions as immutable; publish a new version for revisions.
- Do not include credentials, tokens, private keys, session material, raw
  sensitive logs, or internal details that are unnecessary to the artifact.
- Distinguish observed current state, recommendation, and proposed future state.
- Keep diagrams readable on narrow screens and prevent horizontal overflow.
- Use semantic HTML, visible focus states, sufficient contrast, and useful
  text equivalents for diagrams.
- Do not claim an upload succeeded until the MCP returns the artifact URL and
  committed version.
