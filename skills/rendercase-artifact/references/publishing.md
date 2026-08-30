# Packaging and Rendercase publishing

## Bundle validation

Before publishing:

1. Confirm the entrypoint exists and opens without server-side routing.
2. Confirm local asset references are relative and remain inside the bundle.
3. Search the bundle for credentials, bearer tokens, private keys, cookies,
   environment files, and accidental raw operational data.
4. Check that no unrelated workspace files enter the ZIP.
5. Open or render the entrypoint locally when tooling permits, and check narrow
   and desktop layouts when visual correctness matters.
6. ZIP the contents from inside the artifact directory so the entrypoint is at
   the archive root, not nested below a temporary parent directory.

## MCP publishing choices

- Prefer `rendercase_publish` for a modest ZIP that can be safely passed as
  base64 in one call.
- For a large bundle, call `rendercase_create_upload`, PUT the ZIP to its
  short-lived upload URL with `X-Rendercase-Upload-Token`, then call
  `rendercase_commit_upload`.
- To revise an existing artifact, pass its exact `artifact_id`; committing
  creates the next immutable version.
- Use `rendercase_get` after publishing when an additional verification read is
  warranted.
- `rendercase_share` creates a capability URL. Never call it merely because an
  artifact was published; require explicit sharing intent and apply requested
  expiry or view limits.

Report the normal authenticated artifact URL by default. Never expose upload
tokens or share-management credentials in the response.
