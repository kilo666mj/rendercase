# Authoring visual artifacts

Use the smallest visual form that makes the relationship clear:

- Mermaid flowchart for architecture, ownership, branching, and data flow.
- Mermaid sequence diagram for interactions across time.
- A semantic table for exact responsibility or option comparisons.
- Ordered cards or a timeline for migration plans.

## Diagrams

For Mermaid, include a pinned browser module or bundled script and initialize
it after DOM load. Use a theme matching the page. Put the Mermaid source in a
`pre.mermaid` element so the standalone artifact remains understandable if
rendering fails. Accompany each diagram with a concise prose explanation or
responsibility table; do not make meaning depend only on color or arrow style.

Keep node labels short. Put rationale and caveats outside the diagram. Prefer
left-to-right flows for pipelines and top-to-bottom flows for hierarchies.
Distinguish current and proposed components explicitly rather than blending
them into one unlabeled diagram.

## Layout

- Start with the outcome or proposed boundary.
- Follow with the visual architecture, then responsibility table, migration
  sequence, risks, and open decisions when relevant.
- Use `minmax(0, 1fr)` for responsive grid tracks and `min-width: 0` for grid
  and flex children.
- Wrap long identifiers and constrain wide tables in an overflow container.
- Avoid tiny diagram text and fixed-width desktop-only canvases.
- Keep printing usable with a simple `@media print` rule.

## Design-system selection

Inspect the represented project's existing CSS, templates, theme tokens, or
brand assets first. Reuse only assets safe to include. When there is no design
system, use system fonts, a small CSS variable palette, strong hierarchy, and
minimal animation. Remote libraries are acceptable when needed, but local or
inline assets make the artifact more durable.
