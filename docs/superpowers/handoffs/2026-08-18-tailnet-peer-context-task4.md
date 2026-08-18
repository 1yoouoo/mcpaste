# MCPaste Tailnet Redesign Handoff after Task 4

## Current goal

Replace the hosted MCPaste architecture with a Mac-only, hostless, ephemeral current-context system across N Tailscale-connected Macs. Any Mac may publish the one current text-and-image snapshot. Multiple MCP-compatible model clients read through the local runtime. There is no cloud, S3, account, pairing, history, permanent host, or disk persistence for context content.

## Branch and pushed scope

- Branch: `main`
- Implemented files: `internal/peer/*.go` through Task 4
- Design: `docs/superpowers/specs/2026-08-18-tailnet-peer-context-design.md`
- Plan: `docs/superpowers/plans/2026-08-18-tailnet-peer-context.md`
- Four pre-existing local macOS edits were deliberately excluded from this checkpoint: `ConnectorSetup.swift`, `StatusPopoverView.swift`, `MCPasteAPI.swift`, and `APIClientTests.swift`.

## Completed

- Task 1: deterministic HLC revision model, overflow boundaries, race and JSON contract tests.
- Task 2: atomic one-lock in-memory context store, staged assets, complete snapshot adoption, source-specific reachability, stale-source connector gating, bounds, deep-copy isolation.
- Task 3: Tailscale JSON discovery, online non-sharee filtering, bounded no-shell command runner, content-free atomic `0600` peer registry with no-follow/nonblocking reads.
- Task 4 implementation: exact peer/loopback route table, allowlist and bearer boundaries, strict bounded bodies, asset staging/publication, offline replica responses, health privacy, announce callback, computed device envelope, fixed generic errors.
- Task 4 spec review: approved.

## Required first work on remote

Task 4 final quality review found these unresolved items. Fix them with TDD and re-run spec/quality review before starting Task 5:

1. Replace `Store.httpSnapshot` with manifest-only and indexed-asset accessors. Current health/manifest/devices/announce reads copy the full asset bundle, causing allocation amplification.
2. Compare bearer authorization as fixed-size SHA-256 digests. `subtle.ConstantTimeCompare` on variable-length byte slices leaks supplied/configured length equality.
3. Reject announce revisions beyond the Store's 24-hour future boundary before invoking the callback.
4. Bound and deduplicate announce callback execution: one active callback, same-revision dedupe, different-revision busy response, two-second context timeout.
5. Add `SyncUpdating = "updating"`, require `SyncState`, and whitelist all four approved states; invalid/missing callback must return 503.
6. Authenticate after route-domain identification but before method/configuration disclosure, so unauthorized requests receive 401/403 rather than 405/503.

Suggested write scope for this remediation: `internal/peer/http.go`, `internal/peer/http_test.go`, and narrowly `internal/peer/model.go` plus a contract test for `SyncUpdating`.

## Verified at handoff

- `go test -race -count=1 ./internal/peer` passed immediately before handoff.
- Task 1, Task 2, and Task 3 passed independent spec and quality reviews.
- Task 4 passed independent spec review.
- No commit included the pre-existing macOS working-tree edits.

## Known baseline issue

Before this redesign, `swift test` built and then hung in `ConnectorSetup.outcome(awaiting:stderr:)` at `process.waitUntilExit()`. The user explicitly asked not to investigate because Task 7 replaces/reworks that path. Re-check after Task 7.

## Next sequence

1. Resolve and review the six Task 4 findings above.
2. Execute Task 5 from the implementation plan: N-peer convergence and runtime lifecycle.
3. Continue Tasks 6-12 in order with strict TDD, spec review, quality review, and verification checkpoints.
4. Do not restore hosted/S3/database compatibility layers; this redesign is a replacement.

## Safety and workflow

- Read project instructions and both design/plan documents before editing.
- Work directly on the checked-out branch unless the owner says otherwise.
- Preserve unrelated changes; do not stash, reset, clean, switch branches, or rewrite history without approval.
- Do not push/merge/deploy again without explicit approval.
