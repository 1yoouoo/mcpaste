# Phase 3 text timeline and MCP connector handoff

Date: 2026-08-13

## Scope

Phase 3 is implemented locally on `main` from starting commit `cd934883c8b902a3f1d07e175bd5cec08527d1d4`. The work covers encrypted text pastes, immutable revisions and tombstones, server ordering, idempotent mutations, durable sync, metadata-only SSE, the official Go MCP SDK, the Linux STDIO proxy, Linux credential storage, setup, and duplicate-free Codex/Claude Code configuration. Images, the macOS app, deployment, release, and production infrastructure remain out of scope.

The host does not have the `go` command. All Go commands below were run in Docker with `golang:1.26.5-bookworm`, `GOFLAGS=-mod=readonly`, and the local Compose PostgreSQL service where integration tests required it. The pinned PostgreSQL image and health-checked Compose service were started before testing.

## Local commits

- `bc9892b` — docs: plan text timeline and mcp connector
- `96e52f8` — feat: add text paste schema
- `fca14e6` — feat: add encrypted text paste revisions
- `7cb7048` — feat: expose text sync APIs
- `594fef7` — feat: add durable sync stream
- `4c27352` — feat: expose read-only MCP tool
- `334aebd` — feat: add stdio mcpaste proxy
- `6c59ed0` — feat: configure read-only mcpaste clients
- `35e2176` — fix: bound connector pairing claim
- final documentation commit — this record, CI/readme/security updates, and migration-test expectations

No push, merge, deployment, tag, or release was performed.

## Verification evidence

- Phase 2 baseline: `go test -race ./...` and `go vet ./...` passed before Phase 3 changes against Compose PostgreSQL.
- Migration status and verify: `applied=2 available=2` after `go run ./cmd/migrate up` against the local Compose database.
- Final `go test -race ./... -count=1`: passed for every package, including PostgreSQL integration packages, `internal/httpserver`, `internal/mcpserver`, `internal/connector`, and the real process E2E in `cmd/mcpaste`.
- Final `go vet ./...`: passed.
- Final `go test ./... -count=1`: passed.
- Focused integration run `go test ./... -run 'Integration|MCP|E2E|Paste|Sync' -count=1`: passed.
- `go mod tidy -diff`, `go mod verify`: passed.
- Native builds: `cmd/server`, `cmd/migrate`, and `cmd/mcpaste` built successfully.
- Linux connector builds: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` and `GOARCH=arm64` built successfully with `-trimpath`.
- `docker build -t mcpaste-server:phase3 .`: passed. Image inspection confirmed user `65532:65532` and entrypoint `/mcpaste-server`.
- Gitleaks `v8.24.3` container scan: 54 commits scanned, no leaks found. The repository regex secret-pattern check also returned no matches.
- `git diff --check`: passed at each commit boundary.

The real STDIO test builds and launches the `mcpaste` child process, connects with the official SDK command transport, verifies exactly one tool, retrieves exact text, verifies the structured empty result, and checks that the bearer marker is absent from the URL and child stderr. Tests use visibly fake markers only; no credential or paste body is recorded here.

## Requirement mapping

| Requirement | Evidence |
| --- | --- |
| Exact text persistence and one-year retention | `internal/identity/text_test.go`, `000002_text_pastes`, encrypted `paste_revisions` |
| Immutable revisions and tombstones | `TestUpdateDeleteAndSequenceOrdering`, `TestSyncReturnsOrderedEventsAndTombstones` |
| Purge path | `TestPurgeTextDeletesExpiredRevisionsAndOrphanPastes`, service cleanup integration, cleanup log fields |
| Server sequence last-write-wins and idempotency | text service tests, `AppendTextRevision`, encrypted idempotency replay |
| Durable cursor and incremental sync | `internal/identity/postgres/sync.go`, HTTP sync integration and query tests |
| SSE and polling fallback | `TestSSEEmitsMetadataOnlyAndPollingCatchesUp`, metadata-only invalidation writer, README contract |
| Official SDK `get_latest_paste` | `internal/mcpserver/server.go` and official SDK client tests |
| Exact text and empty MCP results | MCP package tests, authenticated HTTP MCP integration, process E2E |
| Authenticated Streamable HTTP MCP | `/v1/mcp` bearer boundary and full-credential rejection test |
| Read-only connector surface | connector scope rejected on mutations, sync, events, device APIs; one MCP tool only |
| Linux credential storage | mode `0700` directory, mode `0600` file, atomic replacement tests |
| Codex and Claude setup | TOML/JSON fixture tests, preservation and idempotence assertions, setup test |
| Linux amd64/arm64 builds | final cross-build commands and CI build entries |
| PostgreSQL integration | Compose PostgreSQL and per-test schemas through `internal/testdb` |
| Secret/diff checks | Gitleaks, regex scan, `git diff --check`, final status check |

## Remaining risks and unverified items

- The connector setup flow still requires a human administrator to approve the printed pairing request; the automated test uses an in-memory approval response.
- Codex and Claude configuration tests use representative files. A live installation of either client was not modified, and no real credential was used.
- The server and connector permit HTTP endpoints for local tests; production deployment must provide HTTPS as specified by the design.
- No production endpoint, DigitalOcean resource, macOS app, image pipeline, or release artifact was exercised by design.
- The MCP endpoint is stateless and JSON-response based; SDK behavior may evolve with future SDK releases, which is why `go.mod` pins `github.com/modelcontextprotocol/go-sdk v1.7.0`.

The final tree is intended to be clean after the documentation commit. The branch remains local and has not been pushed.
