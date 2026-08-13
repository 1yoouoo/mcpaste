# MCPaste production handoff

Date: 2026-08-14

## Status

- Phases 1–6 are implemented on `main`.
- Production is running at `https://mcpaste.1yoouoo.com` on the DigitalOcean Droplet `mcpaste-prod-sgp1`.
- The public GHCR image is `ghcr.io/1yoouoo/mcpaste`.
- The macOS UI remains intentionally functional but visually unfinished; UI polish is deferred.

## Production evidence

- `livez` and `readyz` return HTTP 200.
- Caddy, PostgreSQL, and the server containers are healthy.
- Four migrations are applied.
- The authenticated production STDIO MCP E2E test retrieved exact remote text, rejected connector writes, exposed only `get_latest_paste`, and found no token in child-process stderr.

## Final review fixes

- Image cleanup now selects at most 100 expired revision trees before filesystem deletion and deletes only those selected trees in PostgreSQL.
- Linux publishes the GitHub release before invoking the reusable macOS workflow; macOS accepts only an explicit `v*` tag and uploads idempotently.
- Swift SSE tests cover event IDs, `Last-Event-ID`, cursor-expired mapping, and consumer cancellation.

## Owner actions remaining

- Configure Apple Developer ID signing/notarization secrets and create a `v*` release tag when a signed macOS artifact is wanted.
- Decide and record the repository's open-source license.
- Keep the recovery code offline; the current production Droplet intentionally has no automated backups.
