# MCPaste Text Timeline and MCP Connector Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Implement Phase 3's text timeline, durable synchronization, authenticated read-only MCP endpoint, Linux mcpaste STDIO proxy, credential storage, and idempotent Codex/Claude Code setup.

**Architecture:** Add one expand-and-contract PostgreSQL migration for logical text pastes and immutable encrypted revisions. Each mutation allocates one workspace-local sequence and event atomically; the latest sequence wins, tombstones hide content immediately, and revision rows are purged after one year. Full-device HTTP APIs own text mutations and sync. Connector credentials can call only the stateless Streamable HTTP MCP endpoint, implemented with the official Go MCP SDK and exposing only get_latest_paste. The mcpaste executable serves a local STDIO MCP server whose sole tool forwards to that endpoint, while setup stores the connector credential in a mode-0600 Linux file and updates AI-client configuration atomically.

**Tech Stack:** Go 1.26.5, PostgreSQL 18.4, pgx v5.10.0, AES-256-GCM, github.com/modelcontextprotocol/go-sdk v1.7.0, github.com/pelletier/go-toml/v2, net/http, JSON, SSE, and os/exec.

**Spec:** docs/superpowers/specs/2026-08-12-mcpaste-system-design.md

## Global Constraints

- Text is stored exactly as received, including line breaks and surrounding whitespace.
- Every accepted text mutation receives a monotonically increasing workspace-local server sequence and server timestamp; server order, never client time, decides last-write-wins.
- Every text revision is immutable, encrypted with AES-256-GCM using a fresh 12-byte nonce, a new text-specific purpose, and a one-year server expiry.
- Deletion creates an immutable tombstone and hides the paste immediately from history, sync snapshots, and MCP; purge removes expired revision ciphertext and metadata after its one-year deadline.
- Idempotency keys replay the exact original status and body and use distinct paste operation names and canonical request hashes.
- Event cursors are durable PostgreSQL workspace sequences retained for 35 days; incremental sync returns a generic cursor-expired response when history is unavailable.
- SSE emits metadata-only invalidations; clients fetch text through incremental sync or polling. SSE must never contain paste text or image bytes.
- The only MCP tool is get_latest_paste; connector scope is read-only and no connector route or tool may create, edit, delete, rename, revoke, pair, or recover.
- MCP text results preserve the exact text in one mcp.TextContent; an empty workspace returns a successful structured empty result, not an error.
- Streamable HTTP authentication uses exactly one bearer credential in the Authorization header; credentials never enter URLs, configuration files, logs, or test output.
- The official Go SDK is used for both the remote Streamable HTTP server and the local STDIO MCP server/client bridge.
- Configuration writes preserve unrelated Codex and Claude Code entries, are idempotent, use atomic replacement, and never write the connector credential into client configuration.
- No image, macOS app, deployment, release, or production infrastructure work is included in Phase 3.
- PostgreSQL integration tests use the existing local Docker Compose service and per-test schemas from internal/testdb.

---

## Contract and file map

The Phase 3 HTTP contract follows the existing /v1/ error, authentication, JSON, and idempotency conventions:

| Method and route | Scope | Purpose |
| --- | --- | --- |
| POST /v1/pastes | full | Create one text paste. Body: {"text": string}. Returns 201 paste metadata and current revision. |
| PATCH /v1/pastes/{paste_id} | full | Create a new immutable text revision. Body: {"text": string}. Returns 200 current revision. |
| DELETE /v1/pastes/{paste_id} | full | Append a tombstone. Returns 204. |
| GET /v1/pastes | full | Return non-deleted text pastes whose current revision was created in the last 30 days, newest first. |
| GET /v1/sync?after=<sequence>&limit=<n> | full | Return durable changes after a cursor, a new cursor, and has_more; return 410 cursor_expired when history is unavailable. |
| GET /v1/events?after=<sequence> | full | Stream metadata-only SSE invalidations, with polling/keepalive behavior and no body content. |
| POST /v1/mcp and GET /v1/mcp | connector | Authenticated official Streamable HTTP MCP transport. GET is allowed only for SDK session behavior. |

Created and revision records contain paste_id, revision_id, kind, server_sequence, created_at, expires_at, deleted, and text only at the authorized full-device sync/history boundary. MCP returns metadata plus one exact text content block.

Files to create or modify:

- Create db/migrations/000002_text_pastes.up.sql and .down.sql.
- Modify internal/identity/model.go, dto.go, and store.go.
- Modify internal/identity/service.go; split into internal/identity/text.go only if the current file becomes responsibility-heavy.
- Create internal/identity/postgres/pastes.go and sync.go.
- Create internal/identity/text_test.go and internal/identity/postgres/text_integration_test.go.
- Modify internal/httpserver/api.go; create internal/httpserver/sync.go, sync_test.go, sse_test.go, and mcp_test.go.
- Create internal/mcpserver/server.go and server_test.go.
- Create internal/connector/credential.go, proxy.go, setup.go, config.go, and matching tests; create cmd/mcpaste/main.go and tests.
- Modify go.mod, go.sum, README.md, docs/security-and-secrets.md, Dockerfile, and .github/workflows/ci.yml only for Phase 3 requirements.

## Task 1: Pin dependencies and define the paste schema

**Files:**

- Modify go.mod and go.sum.
- Create db/migrations/000002_text_pastes.up.sql and .down.sql.
- Create internal/identity/text_test.go and internal/identity/postgres/text_integration_test.go.

**Interfaces:**

- Consumes: Phase 2 workspaces.next_event_sequence, workspace_events, idempotency_records, secure.Keyring, and internal/testdb.
- Produces: migration version 000002_text_pastes and the failing persistence contract.

- [ ] Step 1: Add exact dependencies.

Run in the Go 1.26.5 container:

~~~bash
go get github.com/modelcontextprotocol/go-sdk@v1.7.0 github.com/pelletier/go-toml/v2@v2.2.3
go mod tidy
go mod verify
~~~

Keep existing pgx, x/crypto, x/text, and Go directives unchanged. Confirm the new modules are direct requirements and there is no local replacement.

- [ ] Step 2: Write migration contract tests before SQL.

Assert the migration loader exposes versions 1 and 2; the schema contains pastes and paste_revisions; revision bodies are nullable only for tombstones; event checks accept paste.created, paste.revised, and paste.deleted while retaining Phase 2 event types; and expires_at is exactly created_at plus one year. Insert two revisions for one workspace, verify composite foreign keys, verify a tombstone has no ciphertext, and reject an incorrect retention deadline.

- [ ] Step 3: Run tests red.

~~~bash
go test ./internal/database/migrate ./internal/identity ./internal/identity/postgres -run 'Test(Text|Paste|Migration)' -count=1
~~~

Expected: failure because migration version 2 and its tables do not exist.

- [ ] Step 4: Add the expand-only migration.

Create pastes with workspace foreign key, UUID primary key, created_at, last_mcp_access_at, and mcp_access_count. Create paste_revisions with UUID id, composite workspace/paste identity, server_sequence, created_at, expires_at, revision_kind (content or tombstone), text_key_id, text_nonce, and text_ciphertext. Add checks for one-year expiry, content/tombstone field pairing, 12-byte nonce, non-empty key ID, and non-negative access count. Add indexes for workspace sequence, paste sequence, and expiry cleanup. Extend workspace_events.event_type without dropping existing data.

The down migration removes only Phase 3 objects and restores the Phase 2 event check. It is a local/operator rollback only.

- [ ] Step 5: Make tests green and inspect the boundary.

~~~bash
go test ./internal/database/migrate ./internal/identity/postgres -run 'Test(Text|Paste|Migration)' -count=1
~~~

Inspect pg_indexes and information_schema.columns from the isolated schema. Assert no plaintext text column, image table, or image API is introduced.

- [ ] Step 6: Commit.

~~~bash
git add go.mod go.sum db/migrations/000002_text_pastes.up.sql db/migrations/000002_text_pastes.down.sql internal/identity/text_test.go internal/identity/postgres/text_integration_test.go
git commit -m "feat: add text paste schema"
~~~

## Task 2: Implement encrypted immutable revisions, tombstones, server ordering, and purge

**Files:**

- Modify internal/identity/model.go, dto.go, store.go, and service.go.
- Create internal/identity/postgres/pastes.go and sync.go.
- Modify internal/identity/text_test.go and internal/identity/postgres/text_integration_test.go.

**Interfaces:**

- Consumes: migration 000002 and secure.Keyring.Encrypt/Decrypt.
- Produces: CreatePaste, UpdatePaste, DeletePaste, ListPastes, Sync, LatestPaste, and PurgeText service/store methods.

- [ ] Step 1: Write failing tests first.

Cover:
~~~go
func TestCreatePastePreservesExactText(t *testing.T)
func TestUpdatePasteAddsImmutableRevisionAndServerSequence(t *testing.T)
func TestDeletePasteCreatesTombstoneAndHidesContent(t *testing.T)
func TestOlderOfflineMutationWinsWhenItArrivesLater(t *testing.T)
func TestPasteMutationReplayIsByteIdenticalAndDoesNotDuplicateRevision(t *testing.T)
func TestConnectorPrincipalCannotMutatePaste(t *testing.T)
~~~

Use leading/trailing whitespace, CRLF, Unicode, and an empty string. Assert recovery is byte-for-byte through the service while PostgreSQL sees only ciphertext, nonce, key ID, and hashes.

- [ ] Step 2: Run focused tests red.

~~~bash
go test ./internal/identity ./internal/identity/postgres -run 'Test(CreatePaste|UpdatePaste|DeletePaste|OlderOffline|PasteMutation|ConnectorPrincipal)' -count=1
~~~

Expected: compile failure for missing domain and store methods.

- [ ] Step 3: Add domain values and DTOs.

Define text-only Paste, PasteRevision, PasteMutationInput, SyncEvent, SyncResult, LatestPaste, TextContent, and EmptyLatestPaste values. Keep internal types free of accidental wire tags. Normalize timestamps with the existing UTC-second mapper. Define ErrCursorExpired, ErrPasteExpired, and a generic unavailable-content error without embedding text, SQL, or credential values.

- [ ] Step 4: Implement one transaction-owned sequence allocator.

Update workspaces.next_event_sequence once with returning next_event_sequence, insert the immutable revision with that sequence, and insert exactly one workspace event with the same sequence. Event types are paste.created, paste.revised, and paste.deleted. Do not call the old InsertEvent separately.

- [ ] Step 5: Implement encrypted content and tombstone persistence.

For content, encrypt exact UTF-8 bytes with purpose paste-text and stable object ID <workspace-id>:<paste-id>:<revision-id>. For tombstones, store no body envelope. Query latest valid state by descending server sequence, exclude tombstones and expired content, and never use client timestamps. Decrypt only after authorization and return a generic unavailable-content error for corrupt ciphertext.

- [ ] Step 6: Integrate idempotency.

Use operation names paste.create, paste.update:<paste_id>, and paste.delete:<paste_id>. Hash a canonical struct containing only normalized text for create/update and an empty object for delete. Reuse encrypted exact-response replay; ensure 204 delete replay has an empty body and never creates a second revision. Validate full scope before idempotency and mutation; connector scope returns ErrForbidden.

- [ ] Step 7: Implement bounded retention purge.

Add PurgeText(ctx, now) that deletes revisions with expires_at <= now, then orphan paste rows, returning counts. Call it from existing cleanup. An expired current revision is unavailable before physical purge.

- [ ] Step 8: Run green tests and commit.

~~~bash
go test ./internal/identity ./internal/identity/postgres -run 'Test(CreatePaste|UpdatePaste|DeletePaste|OlderOffline|PasteMutation|ConnectorPrincipal|Text|Paste)' -count=1
git add internal/identity internal/identity/postgres
git commit -m "feat: add encrypted text revisions"
~~~

## Task 3: Expose paste mutations, snapshots, and durable incremental sync

**Files:**

- Modify internal/httpserver/api.go, json.go, identity/service.go, and identity/dto.go.
- Create internal/httpserver/sync.go and sync_test.go.
- Modify internal/httpserver/api_test.go and identity_integration_test.go.

**Interfaces:**

- Consumes: Phase 2 authentication/error/strict JSON conventions and Task 2 service methods.
- Produces: full-device text mutation, history, and incremental sync HTTP contract.

- [ ] Step 1: Write route tests before registration.

Add tests named TestPasteMutationRequiresFullScopeAndIdempotency, TestPasteMutationPreservesWhitespace, TestPasteDeleteReturnsEmpty204, TestPasteHistoryExcludesTombstonesAndExpiredCurrentText, TestSyncReturnsEventsAfterCursorAndAdvancesCursor, TestSyncRejectsMalformedCursorAndLimitWithoutDatabaseAccess, and TestSyncReturnsCursorExpiredWithoutLeakingRetentionDetails.

Assert connectors receive 403 on every mutation, history, and sync route. Deleted events contain no text; full sync returns exact text.

- [ ] Step 2: Run route tests red.

~~~bash
go test ./internal/httpserver -run 'Test(Paste|Sync)' -count=1
~~~

Expected: failure because routes and identity API methods are absent.

- [ ] Step 3: Add strict request validation.

Accept only application/json, one object, no unknown/duplicate fields, no trailing value, and no body byte beyond 4,096. Require exactly one Idempotency-Key for create/update/delete. Parse paste UUID, cursor, and limit before database access.

- [ ] Step 4: Register method-aware routes and handlers.

Register POST /v1/pastes, PATCH /v1/pastes/{paste_id}, DELETE /v1/pastes/{paste_id}, GET /v1/pastes, and GET /v1/sync. Keep v1MethodGuard complete, canonical errors unchanged, and auth failures generic.

- [ ] Step 5: Define cursor semantics.

after is a non-negative decimal sequence defaulting to 0; limit defaults to 100 and is capped at 100. Events are ascending, cursor is the greatest delivered/current sequence, and has_more is explicit. An unavailable cursor returns 410 {"error":{"code":"cursor_expired"}} without retention details. Snapshot is the recovery path.

- [ ] Step 6: Prove full/connector and workspace isolation.

Through PostgreSQL HTTP integration, create/update/delete and replay text as full, read history and sync as full, call every route as connector, and assert no connector mutation changes the database. A second workspace cannot read the first workspace by UUID or cursor.

- [ ] Step 7: Run green tests and commit.

~~~bash
go test ./internal/httpserver ./internal/identity ./internal/identity/postgres -run 'Test(Paste|Sync|IdentityLifecycle|Connector)' -count=1
git add internal/httpserver internal/identity
git commit -m "feat: expose text sync APIs"
~~~

## Task 4: Add SSE invalidation and polling fallback

**Files:**

- Modify internal/httpserver/api.go, identity/store.go, identity/service.go, identity/postgres/sync.go, and cmd/server/main.go.
- Create internal/httpserver/sse_test.go.
- Modify identity_integration_test.go.

**Interfaces:**

- Consumes: GET /v1/sync, durable event sequences, and full-scope authentication.
- Produces: GET /v1/events?after=<cursor> with reconnect-safe metadata-only SSE and documented polling fallback.

- [ ] Step 1: Write SSE/polling tests first.

Cover initial event, reconnect after cursor, keepalive comments, cancellation, auth failure, connector rejection, and a fallback client receiving the same changes by polling /v1/sync. Assert SSE contains no text, authorization, query values, or response bodies.

- [ ] Step 2: Run tests red.

~~~bash
go test ./internal/httpserver -run 'Test(SSE|Polling)' -count=1
~~~

- [ ] Step 3: Implement SSE.

Authenticate full scope, parse after or Last-Event-ID, poll durable events at a bounded interval, and emit only:
~~~text
event: invalidation
id: 42
data: {"sequence":42}

~~~
Flush after each event, send a comment heartbeat at most every 15 seconds, catch up before waiting, and stop on cancellation. Never emit revision text or database errors.

- [ ] Step 4: Wire cleanup and documentation.

Clients poll /v1/sync?after=<cursor> every 15 seconds when SSE is unavailable. Call PurgeText from the existing cleanup worker without changing cancellation order. Document this contract.

- [ ] Step 5: Run race tests and commit.

~~~bash
go test -race ./internal/httpserver ./internal/identity ./internal/identity/postgres -run 'Test(SSE|Polling|Sync|Paste)' -count=1
git add internal/httpserver internal/identity cmd/server/main.go README.md docs/security-and-secrets.md
git commit -m "feat: add durable sync stream"
~~~

## Task 5: Implement the authenticated official-SDK MCP server

**Files:**

- Create internal/mcpserver/server.go and server_test.go.
- Modify internal/httpserver/api.go, identity_integration_test.go, identity/service.go, identity/store.go, and identity/postgres/pastes.go.

**Interfaces:**

- Consumes: connector principal authentication and LatestPaste.
- Produces: authenticated handler at /v1/mcp backed by mcp.Server with exactly one get_latest_paste tool.

- [ ] Step 1: Write SDK contract tests first.

Use an official SDK mcp.Client with StreamableClientTransport against an httptest.Server and runtime credentials. Add TestMCPListsOnlyGetLatestPaste, TestMCPReturnsExactTextContent, TestMCPReturnsStructuredEmptyResult, TestMCPConnectorCannotCallWriteRoutes, and TestMCPRejectsMissingMalformedAndWrongWorkspaceCredentials.

Assert exact text through mcp.TextContent.Text, not JSON re-encoding. Empty means IsError false, empty content, and structured available:false.

- [ ] Step 2: Run tests red.

~~~bash
go test ./internal/mcpserver ./internal/httpserver -run 'TestMCP' -count=1
~~~

Expected: failure because package, route, and SDK integration are absent.

- [ ] Step 3: Add one low-level read tool.

Create mcp.Server with mcp.NewServer(&mcp.Implementation{Name: "mcpaste", Version: ...}, nil). Register exactly one mcp.Tool named get_latest_paste with an explicit empty object input schema and a low-level mcp.ToolHandler. The handler reads the connector principal from context, calls GetLatestPaste, and returns one mcp.TextContent for text or a structured empty object. Do not register write tools, resources, prompts, or completion endpoints.

- [ ] Step 4: Add authenticated stateless Streamable HTTP.

Wrap the SDK handler with the existing auth boundary before /v1/mcp: require exactly one bearer header, authenticate a connector principal, and reject full credentials with 403 for this connector-only boundary. Construct mcp.NewStreamableHTTPHandler with Stateless:true and JSONResponse:true. Protocol errors remain SDK responses; credential failures remain canonical identity errors. Put the principal in request context and never log MCP bodies or headers.

- [ ] Step 5: Verify access metadata and read-only surface.

After success, inspect only last_mcp_access_at and mcp_access_count. Assert the MCP route is unavailable to a full write credential and every paste mutation with connector scope returns 403 without a new revision.

- [ ] Step 6: Run SDK tests and commit.

~~~bash
go test ./internal/mcpserver ./internal/httpserver ./internal/identity -run 'TestMCP|TestPaste' -count=1
git add internal/mcpserver internal/httpserver internal/identity
git commit -m "feat: expose read-only MCP tool"
~~~

## Task 6: Build Linux credential storage and the STDIO proxy

**Files:**

- Create internal/connector/credential.go, proxy.go, credential_test.go, and proxy_test.go.
- Create cmd/mcpaste/main.go and main_test.go.

**Interfaces:**

- Consumes: authenticated remote MCP and the Phase 2 pairing grant shape.
- Produces: default STDIO MCP process and mcpaste setup entry point.

- [ ] Step 1: Write tests first.

Add TestCredentialFileIsMode0600AndAtomicallyReplaced, TestCredentialStoreDoesNotPrintToken, TestProxyListsAndForwardsOnlyGetLatestPaste, TestProxyUsesAuthorizationHeaderWithoutURLToken, and TestProxyPropagatesExactTextAndEmptyResult. Use temporary XDG_CONFIG_HOME and example-token-not-real; never print a generated credential.

- [ ] Step 2: Run tests red.

~~~bash
go test ./internal/connector ./cmd/mcpaste -run 'Test(Credential|Proxy|MCPaste)' -count=1
~~~

- [ ] Step 3: Implement the Linux fallback store.

Store endpoint and connector token in $XDG_CONFIG_HOME/mcpaste/credential.json or ~/.config/mcpaste/credential.json. Directory mode is 0700, file mode 0600, writes use a same-directory temporary file, fsync, close, and rename. Read only into memory. Keep this behind an interface for a future OS store; no token reaches shell history, URL, logs, or client config.

- [ ] Step 4: Implement the SDK proxy.

The default command loads credentials, creates an http.Client whose RoundTripper adds the bearer header, connects an mcp.Client with mcp.StreamableClientTransport, creates a local mcp.Server, and registers only get_latest_paste. The local handler calls the remote session and returns its SDK result unchanged. Connect local server with mcp.StdioTransport; stdout is protocol-only and diagnostics go to stderr without secrets.

- [ ] Step 5: Implement mcpaste setup.

Accept --endpoint, --name, and --credential-file. Create a connector pairing request, display only the non-secret pairing ID/short code/QR payload required for approval, poll the private claim endpoint in memory until approval or expiry, save the grant, and update detected client configuration. Never print the claim secret. No-argument mode is the STDIO MCP server; setup is the recovery entry point.

- [ ] Step 6: Run tests and commit.

~~~bash
go test -race ./internal/connector ./cmd/mcpaste -run 'Test(Credential|Proxy|MCPaste)' -count=1
git add internal/connector cmd/mcpaste
git commit -m "feat: add stdio mcpaste proxy"
~~~

## Task 7: Add idempotent Codex/Claude setup and process E2E

**Files:**

- Create internal/connector/config.go, config_test.go, and setup.go.
- Modify cmd/mcpaste/main.go and main_test.go.
- Create cmd/mcpaste/e2e_test.go.
- Modify Dockerfile, .github/workflows/ci.yml, and README.md.

**Interfaces:**

- Consumes: credential store and setup endpoint from Task 6.
- Produces: duplicate-free configuration writers and real process-level STDIO-to-remote MCP coverage.

- [ ] Step 1: Write configuration tests first.

Use Codex TOML and Claude JSON fixtures containing unrelated servers, an existing unrelated mcpaste name, and an already-correct entry. Assert TestConfigureCodexPreservesUnrelatedServers, TestConfigureClaudePreservesUnrelatedServers, TestConfigureIsIdempotent, TestConfigureChoosesSuffixForUnrelatedMCPasteName, and TestConfigurationReplacementIsAtomicAndModePreserving. The entry has an absolute command path and endpoint argument but no token.

- [ ] Step 2: Run tests red.

~~~bash
go test ./internal/connector -run 'TestConfigure|TestConfiguration' -count=1
~~~

- [ ] Step 3: Implement detection.

Use CODEX_CONFIG_PATH, then $HOME/.codex/config.toml. Use CLAUDE_CONFIG_PATH, then CLAUDE_CONFIG_DIR/.claude.json, then $HOME/.claude.json. Codex writes [mcp_servers.<name>] with command/args; Claude writes the mcpServers.<name> stdio object. Preserve all unrelated fields.

- [ ] Step 4: Implement duplicate-free atomic updates.

Treat an entry as the same MCPaste entry only when command resolves to the same absolute path and its endpoint argument matches. If reserved name is unrelated, select mcpaste-2, mcpaste-3, and so on. Do not rewrite semantically correct files. New files use mode 0600; existing mode is preserved.

- [ ] Step 5: Add the real child-process STDIO integration test.

Start an authenticated official-SDK Streamable HTTP server with deterministic text. Start the real mcpaste child with a temporary credential file, send newline-delimited MCP initialize, tools/list, and tools/call over stdin, and decode stdout as JSON-RPC. Assert exact text, exactly one tool, no token in URL/stderr, and structured empty results in a second case. A write route/tool must be unavailable.

- [ ] Step 6: Add Linux builds.

~~~bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o /tmp/mcpaste-linux-amd64 ./cmd/mcpaste
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o /tmp/mcpaste-linux-arm64 ./cmd/mcpaste
~~~

Extend CI with an amd64/arm64 matrix, keep GOFLAGS=-mod=readonly, and do not add a release/deployment job.

- [ ] Step 7: Document and commit.

Document endpoint, file-mode fallback, config paths, no-token-in-config, mcpaste setup, get_latest_paste, SSE/polling, and builds without any rendered secret.

~~~bash
git add internal/connector cmd/mcpaste Dockerfile .github/workflows/ci.yml README.md
git commit -m "feat: configure read-only mcpaste clients"
~~~

## Task 8: Full Phase 3 acceptance and handoff

**Files:**

- Create docs/superpowers/records/2026-08-13-mcpaste-text-mcp.md.
- Verify all Phase 3 source, tests, migrations, workflow, and docs.

- [ ] Step 1: Run the complete suite.

~~~bash
go test -race ./...
go vet ./...
go build -o /tmp/mcpaste-server ./cmd/server
go build -o /tmp/mcpaste-migrate ./cmd/migrate
go build -o /tmp/mcpaste ./cmd/mcpaste
go test ./... -run 'Integration|MCP|E2E|Paste|Sync' -count=1
~~~

Run in Go 1.26.5 with GOFLAGS=-mod=readonly and Compose PostgreSQL. The full race suite is required.

- [ ] Step 2: Run migration, purge, and isolation acceptance.

Verify applied=2 available=2, create/update/delete text through HTTP, inspect only ciphertext in PostgreSQL, advance a test clock beyond one year, run cleanup, and assert expired rows are gone. Assert latest excludes tombstones and expired text before physical purge.

- [ ] Step 3: Run real STDIO E2E.

Run the process-level test with a temporary credential directory. Verify tools/list exposes exactly one tool and tools/call returns exact text and the structured empty result. Do not print child stderr, bearer values, pairing secrets, or paste bodies outside assertions.

- [ ] Step 4: Verify builds and CI boundaries.

~~~bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o /tmp/mcpaste-linux-amd64 ./cmd/mcpaste
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o /tmp/mcpaste-linux-arm64 ./cmd/mcpaste
docker build -t mcpaste-server:phase3 .
ruby -e 'require "yaml"; YAML.parse_file(".github/workflows/ci.yml")'
~~~

Confirm the server image remains non-root, CI covers both Linux architectures, and no deployment job or credential was added.

- [ ] Step 5: Run secret and diff checks.

~~~bash
git diff --check HEAD
rg -n -e 'sk-[A-Za-z0-9]{12,}' -e 'ghp_[A-Za-z0-9]{12,}' -e 'AKIA[0-9A-Z]{16}' -e 'BEGIN (RSA|OPENSSH|EC) PRIVATE KEY' .
git status --short --branch --untracked-files=all
git diff --stat HEAD
~~~

The secret-pattern command must return no matches. Remove only disposable outputs and temporary credential directories.

- [ ] Step 6: Map every completion condition.

Map text persistence, immutable revisions, tombstones, one-year purge, sequence ordering, idempotency, cursor/sync, SSE, polling, official SDK text/empty MCP, authenticated Streamable HTTP, STDIO proxy, Linux credential mode, Codex/Claude setup, Linux builds, connector read-only authorization, child-process STDIO retrieval, race, vet, build, PostgreSQL integration, secret scan, and diff to a test or command. Report any gap instead of assuming it is covered.

- [ ] Step 7: Record evidence and commit.

Create docs/superpowers/records/2026-08-13-mcpaste-text-mcp.md with exact commands, exit statuses, commit subjects, the host-Go absence/Docker-Go evidence, remaining risks, and unverified items. Never record secrets or paste bodies.

~~~bash
git add docs/superpowers/records/2026-08-13-mcpaste-text-mcp.md
git commit -m "docs: record text mcp phase handoff"
~~~

No push or deployment. Final tree must be clean.

## Plan self-review

- Coverage: Tasks 1-2 cover encrypted text, revisions, tombstones, retention, purge, sequence ordering, and idempotency; Tasks 3-4 cover snapshots, cursors, sync, SSE, and polling; Task 5 covers official SDK MCP server, text/empty results, authentication, and connector read-only enforcement; Tasks 6-7 cover Linux storage, STDIO proxy, setup, configuration, E2E, and both Linux builds; Task 8 covers all gates and evidence.
- Scope contains no image or macOS implementation.
- Every new production boundary has a failing-test step before implementation and a focused green command afterward.
- All named Phase 3 APIs are defined in this plan: CreatePaste, UpdatePaste, DeletePaste, ListPastes, Sync, LatestPaste, PurgeText, /v1/mcp, get_latest_paste, and mcpaste setup.
- Placeholder scan was performed; the plan contains no unresolved decisions or deferred implementation steps.
