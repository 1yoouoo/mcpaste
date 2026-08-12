# MCPaste Identity Server Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build Phase 2's PostgreSQL-backed anonymous workspace identity, encrypted credential issuance, full and connector device pairing, device administration, recovery rotation, authorization, rate limiting, and integration-test boundary without adding paste or MCP behavior.

**Architecture:** One Go 1.26.5 `net/http` service uses pgx v5 against PostgreSQL 18, with repository methods that scope workspace rows by an explicit workspace ID and small services that own transactions. Random 256-bit bearer and claim secrets are returned once through encrypted, replayable onboarding/idempotency records; device secrets are otherwise retained only as SHA-256 hashes, while recovery uses a non-secret locator and an Argon2id verifier. Ordered SQL migrations are embedded in the migration binary, applied one transaction at a time under an advisory lock, and verified by SHA-256 checksum before the server starts.

**Tech Stack:** Go 1.26.5, standard `net/http`, `crypto/aes`, `crypto/cipher`, `crypto/rand`, pgx v5.10.0, `golang.org/x/crypto` v0.55.0 Argon2id, PostgreSQL 18.4, Docker Compose, GitHub Actions

---

## Execution boundary and success criteria

Execute this plan from `/Users/blanc/Documents/Project/mcpaste` on a clean current `main` branch after the documentation-only commit that adds `docs/superpowers/records/2026-08-12-mcpaste-foundation.md` and this Phase 2 plan. Commit `4084a5f` is the required Foundation ancestor, not the required `HEAD`: execution must prove `git merge-base --is-ancestor 4084a5f HEAD`, and the Foundation record plus `docs/superpowers/plans/2026-08-12-mcpaste-identity-server.md` must be the only paths added or changed between `4084a5f` and the execution baseline. Go is 1.26.5 and Docker is 28.2.2 at plan-writing time. Project-local `AGENTS.md` and `.agents/skills/` do not exist at plan-writing time; if either appears before execution, read it before Task 1 and let its more-specific rules override this plan.

Phase 2 is complete only when all of these statements are proved by named tests and final commands:

- an unauthenticated, rate-limited request creates one anonymous workspace, one full Mac device, exactly one full credential, exactly one connector credential, and one shown-once recovery code;
- every bearer secret and private pairing claim secret contains 256 random bits, only a non-secret locator is used for indexed lookup, and the database stores only fixed-length hashes;
- every recovery verifier uses Argon2id with an indexed non-secret locator, 16-byte salt, 64 MiB memory, three passes, four threads, and a 32-byte verifier;
- pairing identifiers and eight-character short codes expire five minutes after request creation and carry no bearer or claim secret;
- approval by a full device creates either one connector credential or exactly two credentials for a full Mac, and an encrypted five-minute grant lets repeated claims return byte-for-byte identical credentials after a dropped response;
- full and connector credentials have separate scopes, connector credentials receive `403 forbidden` from every identity mutation and administration route, and revocation invalidates every credential attached to that device;
- display names are trimmed, at most 80 Unicode code points, free of control characters, unique case-insensitively within a workspace, and receive the smallest available ` (2)`, ` (3)` suffix while internal UUIDs remain globally unique;
- recovery adds a new full Mac, rotates the code atomically, invalidates the old code immediately, preserves existing devices, and supports encrypted idempotent replay of the rotated response;
- a full credential cannot revoke its own current device; the service returns `400 invalid_request` before idempotency or mutation, and another non-revoked full device in the same workspace must perform that revocation;
- every PostgreSQL repository method that reads or mutates an established workspace accepts `workspaceID string` explicitly and includes it in SQL predicates; the only locator-first operations are pre-workspace pairing creation/claim and recovery-code parsing before the explicit workspace-scoped verifier query;
- strict JSON parsing, exact body limits, stable metadata-only errors, trusted-proxy-aware IP extraction, PostgreSQL rate limits, cross-workspace rejection, database readiness, and access-log secret exclusion all have automated coverage;
- migration status reports valid partial state without calling it current; server startup and `/readyz` reject zero available migrations, unapplied migrations, unknown versions, and checksum drift while `/readyz` also requires a successful PostgreSQL ping;
- local and CI PostgreSQL use `postgres:18.4-alpine3.24@sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15`, verified on 2026-08-12 as the current `postgres:18-alpine` multi-architecture index;
- no paste, text, image, MCP, SSE, synchronization delivery, macOS UI, Linux companion, deployment, release, or production infrastructure is introduced.

Do not create a worktree, switch branches, pull, push, deploy, create a Droplet, or change a remote while executing this plan. Do not commit generated local keys, database volumes, request samples containing real data, or test output containing issued credentials.

## Explicit non-goals

This phase creates the durable `events` and `idempotency` foundations required by later phases, but it does not expose event cursors or use idempotency for paste mutations. It creates no paste/text/image tables or APIs, no MCP tool or transport, no Streamable HTTP route, no SSE route, no sync delivery, no macOS code or UI, no Linux companion, no production Compose file, and no deployment workflow. The only Compose service in this phase is local PostgreSQL. The CI workflow remains test-only.

## File map and responsibility lock

Create or modify exactly the files listed here during execution. Each file has one responsibility; do not move these responsibilities into a framework, ORM, generated query layer, or multipurpose package.

### Dependency, local database, configuration, and documentation

- `go.mod`: keep module and Go directives; require pgx v5.10.0 and x/crypto v0.55.0.
- `go.sum`: Go-generated checksums for the exact dependency graph.
- `compose.yaml`: local-only PostgreSQL 18.4 service bound to loopback with a named volume and health check.
- `.env.example`: non-sensitive Phase 2 variable names and visibly local database defaults; never contain an encryption key.
- `internal/config/config.go`: parse database URL, active key identifier, keyring text, cleanup interval, and trusted proxy CIDRs from process environment.
- `internal/config/config_test.go`: prove required values, production proxy checks, key syntax, and Foundation settings.
- `README.md`: exact local database, key-generation, migration, server, and API-boundary commands.
- `docs/security-and-secrets.md`: keyring, token, recovery, pairing, idempotency, and log boundaries.

### Database and migration boundary

- `db/migrations/embed.go`: expose only the embedded ordered SQL filesystem.
- `db/migrations/000001_identity.up.sql`: create all Phase 2 tables, constraints, and indexes.
- `db/migrations/000001_identity.down.sql`: remove only Phase 2 objects in reverse dependency order.
- `internal/database/pool.go`: construct pgxpool, verify initial connectivity, and provide readiness ping.
- `internal/database/pool_test.go`: prove pool configuration rejects malformed URLs without logging them.
- `internal/database/migrate/migrate.go`: parse embedded filenames, checksum up files, report valid status, require exact currentness, advisory-lock migration execution, and bounded rollback.
- `internal/database/migrate/migrate_test.go`: unit-test ordering, malformed names, duplicate versions, checksum mismatch, zero-available rejection, and exact currentness.
- `internal/database/migrate/schema_integration_test.go`: define the schema contract before SQL exists and prove all Phase 2 tables, claim invalidation, and named indexes.
- `internal/database/migrate/migrate_integration_test.go`: prove up, status, checksum refusal, one-step down, and re-up against PostgreSQL.
- `internal/testdb/testdb.go`: create an isolated PostgreSQL schema per integration test, apply migrations, expose a pool, and drop only that schema during cleanup.
- `cmd/migrate/main.go`: small repository-owned `up`, `status`, `verify`, and `down --steps 1` command.

### Security primitives

- `internal/secure/random.go`: define the injectable random-source seam and production system source.
- `internal/secure/base64.go`: single expected-length, strict, round-trip canonical raw-URL-base64 decoder shared by every secret/key parser.
- `internal/secure/envelope.go`: AES-256-GCM keyring, 12-byte unique nonce generation, key identifiers authenticated in associated data, duplicate-key-material rejection, and typed envelopes.
- `internal/secure/envelope_test.go`: round trip, nonce uniqueness, tamper failure, key-ID/context authentication, duplicate-key rejection, and retained-old-key rotation.
- `internal/secure/credential.go`: full/connector bearer generation, parse, locator extraction, and SHA-256 hashing.
- `internal/secure/credential_test.go`: exact 256-bit secret and 128-bit locator format, malformed and noncanonical input rejection, deterministic seam, and hash stability.
- `internal/secure/argon2.go`: process-wide capacity-two limiter, exported opaque concrete permit handle containing only an unexported shared-state pointer, and the only production call site for `argon2.IDKey`.
- `internal/secure/argon2_test.go`: exact process capacity, per-permit derivation serialization, release-during-derivation blocking, exactly-once slot release, cancellation, and post-cancellation reuse proof with fake derivations.
- `internal/secure/recovery.go`: canonical recovery generation/parse and context-aware Argon2id verifier construction/check.
- `internal/secure/recovery_test.go`: correct code, wrong code, malformed/noncanonical code, corrupt verifier, locator mismatch, parameter rejection, and cancellation.

### Identity domain and PostgreSQL repository

- `internal/identity/model.go`: internal domain values, persistence records, errors, clock interface, and exact durations/limits; internal devices have no JSON tags.
- `internal/identity/dto.go`: wire-only grant/device-summary/pairing DTOs, exact JSON tags, device mappers, and UTC-second timestamp normalization.
- `internal/identity/dto_test.go`: exact grant and summary field sets, mandatory `is_current`, and no-fraction timestamp JSON.
- `internal/identity/naming.go`: validate names and allocate workspace-local case-insensitive suffixes.
- `internal/identity/naming_test.go`: Unicode length, controls, case collisions, and suffix allocation.
- `internal/identity/store.go`: repository interface; every established-workspace method takes `workspaceID string` first after context.
- `internal/identity/postgres/store.go`: concrete pgx pool and transaction helper.
- `internal/identity/postgres/idempotency.go`: encrypted response reservation/replay by non-secret scope, operation, and hashed key with database-assigned retention timestamps.
- `internal/identity/postgres/rate_limit.go`: fixed-window PostgreSQL counters over hashed subjects.
- `internal/identity/postgres/onboarding.go`: transactional anonymous create and recovery rotation.
- `internal/identity/postgres/auth.go`: workspace-and-token-locator credential lookup and last-used metadata update.
- `internal/identity/postgres/pairing.go`: pairing request, details, short-code lookup, approval, transaction-held claim locking/marking, encrypted grant replay, and expiry.
- `internal/identity/postgres/devices.go`: scoped list, rename, revoke, credential revocation, and identity events.
- `internal/identity/postgres/cleanup.go`: lock expired unclaimed pairings, atomically revoke their devices/credentials and append workspace events, mark claim invalidation, then purge terminal pairing, idempotency, event, and rate-limit state.
- `internal/identity/postgres/store_integration_test.go`: database constraints, exact credential counts, pairing replay, naming, recovery, idempotency, rate-limit, and isolation proofs.

### Identity service and HTTP boundary

- `internal/identity/service.go`: validate inputs, generate secrets, map DTOs, encrypt replay bodies, decrypt claim grants while the pairing lock is held, call transactional repository methods, and map domain errors.
- `internal/identity/service_test.go`: deterministic clock/random tests for expiry, credential counts/order, and generic failures.
- `internal/httpserver/api.go`: method-aware route registration and handlers for the exact Phase 2 contract.
- `internal/httpserver/json.go`: 4 KiB strict JSON decoder, empty-body check, stable JSON errors, and response writer.
- `internal/httpserver/auth.go`: parse bearer format, call workspace-scoped authentication, and enforce full scope.
- `internal/httpserver/clientip.go`: trust forwarding headers only from configured proxy CIDRs and hash rate-limit subjects.
- `internal/httpserver/api_test.go`: handler contract and strict decoding tests with a fake identity service.
- `internal/httpserver/identity_integration_test.go`: full HTTP-to-PostgreSQL acceptance flow and log-redaction checks.
- `internal/httpserver/health.go`: retain `/livez` behavior and route `/readyz` to PostgreSQL ping.
- `internal/httpserver/health_test.go`: retain Foundation tests and prove database readiness failure is generic.
- `internal/httpserver/logging.go`: preserve safe `r.Pattern` logging unchanged except for an injectable clock if needed by tests.
- `internal/httpserver/logging_test.go`: retain all Foundation assertions and add every Phase 2 secret marker.
- `cmd/server/main.go`: load config, require current migrations at startup/readiness, create pool/keyring/service/handler, run cleanup loop, and close the pool on shutdown.
- `cmd/server/main_test.go`: prove unmigrated startup and readiness fail generically while migrated readiness succeeds.

### Packaging and CI

- `.dockerignore`: keep default-deny behavior and allow only `go.mod`, `go.sum`, `cmd/`, `internal/`, and `db/` inputs needed to build two binaries.
- `Dockerfile`: build and copy `/mcpaste-server` plus separately invokable `/mcpaste-migrate`; keep the server as the default non-root entry point.
- `.github/workflows/ci.yml`: attach pinned PostgreSQL to Go checks and run migration/integration coverage while preserving pinned actions, read-only permissions, disabled checkout credentials, checksum-verified Gitleaks, and no deploy job.

## Fixed domain and security contract

These values are implementation inputs, not tunable defaults:

| Item | Exact value |
| --- | --- |
| JSON request limit | 4,096 bytes after HTTP framing; `http.MaxBytesReader` rejects byte 4,097 |
| JSON media type | `application/json`; parameters such as `charset=utf-8` are accepted through `mime.ParseMediaType` |
| JSON decoding | exactly one non-null top-level object, unknown fields rejected, no trailing non-whitespace JSON value |
| Bearer secret | 32 random bytes, raw URL-safe base64 without padding |
| Bearer locator | 16 random bytes, raw URL-safe base64 without padding; indexed with workspace UUID |
| Bearer wire form | `mcp1.<workspace_uuid>.<locator>.<secret>` |
| Stored bearer material | `SHA-256("mcpaste-credential-v1" || 0x00 || secret_bytes)` only |
| Recovery secret | 32 random bytes, raw URL-safe base64 without padding |
| Recovery locator | 16 random bytes, raw URL-safe base64 without padding; indexed with workspace UUID |
| Recovery wire form | `mcr1.<workspace_uuid>.<locator>.<secret>` |
| Recovery verifier | Argon2id version 19, time 3, memory 65,536 KiB, threads 4, random 16-byte salt, 32-byte output |
| Pairing claim secret | 32 random bytes, returned only in the create response body; stored as SHA-256 with domain separator |
| Pairing short code | 8 uniformly selected symbols from `23456789ABCDEFGHJKMNPQRSTUVWXYZ` |
| Pairing QR payload | `mcpaste://pair/<pairing_uuid>` and no other field |
| Pairing request lifetime | exactly 5 minutes from database `created_at`; ID and short-code details return `410` after this cutoff whether pending or approved, and approval cannot first occur after it |
| Approved claim grant lifetime | exactly 5 minutes from `approved_at`; byte-identical replay throughout that window |
| Idempotency key | client-generated UUID in `Idempotency-Key`; database stores its domain-separated SHA-256 only |
| Idempotency replay lifetime | exactly 24 hours from database `created_at`, assigned with `clock_timestamp()` in the row-insert statement inside the mutation transaction; `expires_at = created_at + interval '24 hours'` |
| Event retention | 35 days; identity metadata only, no request or response body |
| Pairing metadata retention | 24 hours after the later of request expiry or claim-grant expiry |
| Rate-limit row retention | 24 hours after its fixed window closes |
| AES | AES-256-GCM, 32-byte key, 12 random nonce bytes, 16-byte tag, key ID stored beside nonce/ciphertext and authenticated in AAD |
| Encryption associated data | `mcpaste:v1:<key-id>:<purpose>:<stable-object-id>` encoded as UTF-8 |
| Keyring environment | `MCPASTE_ENCRYPTION_KEYS` is comma-separated `<key-id>:<rawurl-base64-32-byte-key>`; `MCPASTE_ACTIVE_KEY_ID` selects writes |
| Keyring uniqueness | key IDs and decoded 32-byte key values are both unique; one canonical decoder rejects wrong length, CR, LF, padding, non-zero trailing-bit aliases, and duplicate bytes under another ID |
| Production key source | process environment only, populated from `/etc/mcpaste/server.env` in Phase 6; no file-reading or default-key code |
| Argon2 admission | one process-wide limiter of capacity 2; an exported opaque concrete `RecoveryPermit` handle contains only a pointer to package-owned state, so copied handles share one mutex/released flag and exact concrete API parameters reject external wrapper types; recovery mutation acquires before any service transaction, precomputes rotation outside the transaction, and passes the same held handle into locked verification without reacquiring |

Go 1.26 no longer treats replacement of `crypto/rand.Reader` as a reliable test seam. Production code must therefore use a `secure.Random` interface whose `SystemRandom.Read` calls `crypto/rand.Read`; tests inject a deterministic reader directly into constructors. Never set `GODEBUG=cryptocustomrand=1` and never commit a rendered bearer, claim secret, recovery code, or encryption key fixture.

### Rate-limit policy

The repository uses fixed database-backed windows so all callers of the single service process see one state and a restart does not clear abuse counters. Subjects are domain-separated SHA-256 values; raw IP addresses, short codes, pairing IDs, recovery locators, and tokens are not stored in `rate_limit_buckets`.

| Action | Subject | Limit and window | Result |
| --- | --- | --- | --- |
| anonymous workspace create | client IP | 5 per 60 minutes | `429 rate_limited` |
| pairing request create | client IP | 10 per 10 minutes | `429 rate_limited` |
| short-code lookup | authenticated workspace plus full-device ID | 30 per 5 minutes | `429 rate_limited` |
| pairing claim | client IP and pairing ID, counted separately | 10 per 5 minutes for each subject | `429 rate_limited` |
| recovery | client IP and recovery locator, counted separately | 5 per 30 minutes for each subject | `429 rate_limited` |

Every `429` includes an integer `Retry-After` header equal to the ceiling of seconds until the fixed window resets. Rate limiting occurs before Argon2id. Idempotent completed-response replay occurs before charging a second mutation attempt, so a network retry with the same key does not consume another quota unit.

An eight-character short-code lookup is syntactically validated against the fixed alphabet before any rate-limit write or pairing SQL lookup. Wrong-length, lowercase, ambiguous (`0`, `1`, `I`, `L`, `O`), punctuation, or non-ASCII input returns `400 invalid_request` without consuming quota.

### Credential and response-loss invariants

1. Workspace creation, pairing-request creation, approval, rename, revoke, and recovery require exactly one `Idempotency-Key`. The key is never logged. Workspace and pairing creation use the literal non-secret scope `public`; approval, rename, revoke, and recovery use the workspace UUID as the non-secret scope. The operations hash their canonical request struct; an empty canonical object is used for approve and revoke.
2. A completed idempotency row contains scope ID, response workspace ID when one exists, response status, media type, active key ID, nonce, and encrypted exact response bytes. A retry with the same scope/operation/key/request hash decrypts and returns those exact bytes. A different canonical request in the same scope returns `409 idempotency_conflict`; the same key in two workspace scopes is independent.
3. Idempotency lookup selects and locks `(scope_id,operation,key_hash)` regardless of logical expiry. SQL computes `expired` against `clock_timestamp()`. If expired, the mutation transaction deletes it before invoking any domain mutation and inserts the replacement under the same primary key before commit; a 24-hour key reuse can neither replay stale bytes nor fail as a duplicate-key `500`. The insert statement assigns one database `created_at` and derives `expires_at` exactly 24 hours later.
4. Workspace creation and recovery write workspace/device/credential/recovery rows, an identity event, and the encrypted response in one PostgreSQL transaction. No committed state can exist without a replay record. New workspace verifiers acquire and release the process permit before the workspace mutation transaction. Recovery acquires one opaque concrete permit handle before preflight or rate/database work, precomputes the rotated verifier before opening mutation, then enters at most one of two admitted recovery mutation transactions, re-reads and locks the current verifier row, and verifies through a handle sharing the same package-owned state pointer before rotating. That state's mutex spans each complete derivation and excludes `Release`; sequential generation then verification is allowed, concurrent use serializes, copied handles cannot double-release the slot, the locked verification path cannot reacquire or wait for admission, and every return path releases the permit.
5. Pairing approval locks the pairing row, creates the device and hashed credential rows, marshals the exact claim response once, encrypts it as purpose `pairing-grant`, and commits all fields together. Approval never returns a bearer credential.
6. Claim opens one service-owned transaction, locks the pairing row, verifies the private claim hash and expiry, and decrypts the stored grant while the lock remains held. Only successful decryption is followed by `claimed_at = coalesce(claimed_at, now)` and commit. Tamper or a missing key ID rolls back without setting `claimed_at`, leaving the unclaimed expired grant eligible for cleanup. Every successful repeat before `claim_expires_at` returns byte-for-byte identical JSON.
7. Cleanup selects at most 100 expired, approved, unclaimed rows with `FOR UPDATE SKIP LOCKED`. For each row it revokes the workspace-scoped device and credentials, appends `device.revoked`, and sets `claim_invalidated_at` in the same transaction before terminal metadata may be purged. Claim uses a conflicting row lock: claim wins and cleanup skips, or cleanup wins and claim returns `410 pairing_expired`; no interleaving returns a revoked grant.
8. A full device cannot revoke itself. Validation returns `400 invalid_request` before idempotency lookup or mutation; another active full device in that workspace must revoke it, preserving authenticated replay semantics for allowed revocations.
9. Credential database rows never contain token text or secret bytes. Recovery rows never contain recovery text or secret bytes. Pairing rows never contain claim text or bytes, only the domain-separated hash.

## Exact HTTP API contract

All wire timestamps are normalized with `UTC().Truncate(time.Second)` before marshaling and therefore use UTC RFC 3339 with no fractional seconds. UUIDs use lowercase canonical text. Every JSON response ends with one newline. Grant devices and device summaries are separate DTOs: grant devices contain exactly `id`, `display_name`, `platform`, and `role`; summaries add `created_at` and always serialize `is_current`, including `false`. All authenticated routes require exactly one `Authorization: Bearer <token>` header. No credential, recovery code, short code, pairing QR payload, claim secret, idempotency key, request body, query string, cookie, or forwarded-for header may enter logs.

Successful credential objects always use `kind` values `full` or `connector`. A full Mac response always orders the full credential first and connector credential second. Connector-device responses contain exactly one connector credential.

| Method and route | Auth | Idempotency | Request body | Success response | Errors specific to route |
| --- | --- | --- | --- | --- | --- |
| `GET /livez` | none | none | empty | `200 {"status":"ok"}` | `405` with `Allow: GET` preserves Foundation behavior |
| `GET /readyz` | none | none | empty | `200 {"status":"ok"}` after PostgreSQL ping and `applied == available > 0` | `503 {"status":"unavailable"}`; `405` preserves Foundation behavior |
| `POST /v1/workspaces` | none | required | `{"device_name":string,"platform":"macos"}` | `201` workspace grant shape below | `400 invalid_request`, `409 idempotency_conflict`, `429 rate_limited` |
| `POST /v1/pairing-requests` | none | required | `{"proposed_name":string,"platform":"macos"|"linux","requested_scope":"full"|"connector"}` | `201` pairing-create shape below | `400 invalid_request`, `409 idempotency_conflict`, `429 rate_limited` |
| `GET /v1/pairing-requests/{pairing_id}` | full | none | empty | `200` pairing-details shape | `400 invalid_request` for malformed pairing UUID, `401 unauthorized`, `403 forbidden`, `404 not_found`, `410 pairing_expired`; `HEAD` returns `405` with `Allow: GET` |
| `POST /v1/pairing-requests/lookup` | full | none | `{"short_code":string}` | `200` pairing-details shape | `400 invalid_request`, `401 unauthorized`, `403 forbidden`, `404 not_found`, `410 pairing_expired`, `429 rate_limited` |
| `POST /v1/pairing-requests/{pairing_id}/approve` | full | required | `{}` | `200 {"pairing_id":uuid,"status":"approved","claim_expires_at":timestamp}` | `400 invalid_request`, `401 unauthorized`, `403 forbidden`, `404 not_found`, `409 pairing_already_approved`, `410 pairing_expired`, `409 idempotency_conflict` |
| `POST /v1/pairing-requests/{pairing_id}/claim` | none | none | `{"claim_secret":string}` | `200` stored pairing grant shape | `400 invalid_request`, `401 invalid_claim`, `404 not_found`, `409 pairing_pending`, `410 pairing_expired`, `429 rate_limited` |
| `GET /v1/devices` | full | none | empty | `200 {"devices":[device summary]}` sorted by `created_at,id` | `401 unauthorized`, `403 forbidden`; `HEAD` returns `405` with `Allow: GET` |
| `PATCH /v1/devices/{device_id}` | full | required | `{"display_name":string}` | `200 {"device":device summary}` | `400 invalid_request`, `401 unauthorized`, `403 forbidden`, `404 not_found`, `409 idempotency_conflict` |
| `DELETE /v1/devices/{device_id}` | full; target must differ from current device | required | empty | `204` empty | `400 invalid_request` for malformed ID or self-revocation; `401 unauthorized`, `403 forbidden`, `404 not_found`, `409 idempotency_conflict` |
| `POST /v1/recoveries` | none | required | `{"recovery_code":string,"device_name":string,"platform":"macos"}` | `201` workspace grant shape with rotated recovery code | `400 invalid_request`, `401 invalid_recovery`, `409 idempotency_conflict`, `429 rate_limited` |

The workspace and recovery success body is exactly:

```json
{
  "workspace_id": "00000000-0000-4000-8000-000000000001",
  "device": {
    "id": "00000000-0000-4000-8000-000000000002",
    "display_name": "MacBook Pro",
    "platform": "macos",
    "role": "full"
  },
  "credentials": [
    {"kind": "full", "token": "runtime-secret-is-not-shown"},
    {"kind": "connector", "token": "runtime-secret-is-not-shown"}
  ],
  "recovery_code": "runtime-secret-is-not-shown"
}
```

The `runtime-secret-is-not-shown` string is deliberately invalid and must never be accepted by token parsers or copied into fixtures. HTTP integration tests decode runtime values into variables and never print them.

The pairing-create body is exactly:

```json
{
  "pairing_id": "00000000-0000-4000-8000-000000000003",
  "qr_payload": "mcpaste://pair/00000000-0000-4000-8000-000000000003",
  "short_code": "not-stored-here",
  "claim_secret": "runtime-secret-is-not-shown",
  "expires_at": "2026-08-12T12:05:00Z"
}
```

The pairing-details body is exactly:

```json
{
  "pairing_id": "00000000-0000-4000-8000-000000000003",
  "proposed_name": "Build Host",
  "platform": "linux",
  "requested_scope": "connector",
  "status": "pending",
  "expires_at": "2026-08-12T12:05:00Z"
}
```

After approval, `status` is `approved` and the same response adds `claim_expires_at`. It never adds workspace ID, approver ID, credentials, or a claim secret.

The stored full pairing grant has the workspace-grant shape without `recovery_code`. The stored connector grant is:

```json
{
  "workspace_id": "00000000-0000-4000-8000-000000000001",
  "device": {
    "id": "00000000-0000-4000-8000-000000000004",
    "display_name": "Build Host",
    "platform": "linux",
    "role": "connector"
  },
  "credentials": [
    {"kind": "connector", "token": "runtime-secret-is-not-shown"}
  ]
}
```

Device summaries never contain credentials:

```json
{
  "id": "00000000-0000-4000-8000-000000000002",
  "display_name": "MacBook Pro",
  "platform": "macos",
  "role": "full",
  "created_at": "2026-08-12T12:00:00Z",
  "is_current": true
}
```

Every `/v1/` error uses exactly this metadata-only shape and no `message`, reflected input, object name, database detail, or validation field:

```json
{"error":{"code":"invalid_request"}}
```

The stable code set is `invalid_request`, `unauthorized`, `forbidden`, `not_found`, `idempotency_conflict`, `pairing_pending`, `pairing_already_approved`, `pairing_expired`, `invalid_claim`, `invalid_recovery`, `rate_limited`, `unavailable`, and `internal_error`. Unsupported methods on `/v1/` return `405 {"error":{"code":"invalid_request"}}` with an exact `Allow` header. A complete outer `v1MethodGuard` classifies the exact Phase 2 static and dynamic route shapes before delegation, rejects `HEAD` on both GET shapes, and keeps the inner `ServeMux` limited to method-aware allowed patterns; no methodless per-route fallback is registered. Unknown `/v1/` paths return `404 not_found`.

## Schema, indexes, retention, and rollback contract

Migration `000001_identity` is one expand-only production transaction. The migration command creates and locks `schema_migrations` separately, then runs each up or down file in its own transaction. Application rollback never invokes migration down: the old application must continue to tolerate this additive schema. `down --steps 1` is an explicit local/operator recovery tool and drops all Phase 2 identity state, so it must print the selected version and require the exact `--steps 1` argument. A later production contract migration belongs in a later numbered file only after every deployed application has stopped using the old shape.

`schema_migrations` has `version bigint PRIMARY KEY`, `name text NOT NULL`, `checksum char(64) NOT NULL`, and `applied_at timestamptz NOT NULL`. The checksum is lowercase SHA-256 of the exact embedded up-file bytes. `Status` validates checksums/order and returns `{Applied,Available}` even when valid migrations remain unapplied. `RequireCurrent` rejects `Available == 0` and rejects `len(Applied) != Available`; it also propagates checksum/order/unknown-version failures from `Status`. `up` may consume valid partial status, `status` may report it, while `verify`, server startup, and readiness require currentness. A session-level advisory lock with key `hashtextextended('mcpaste-schema-migrations-v1', 0)` serializes migration processes; the same acquired pgx connection owns the lock until explicit unlock in a deferred function.

The identity migration creates these exact keys and access paths:

| Table | Primary/unique constraints | Workspace and cleanup indexes | Retention |
| --- | --- | --- | --- |
| `workspaces` | UUID primary key | none | workspace lifetime |
| `devices` | UUID primary key; unique `(workspace_id,id)`; unique index `(workspace_id,lower(display_name))` | `(workspace_id,created_at,id)`; `(workspace_id,revoked_at)` | workspace lifetime; revocation is retained metadata |
| `credentials` | UUID primary key; unique `(workspace_id,token_id)`; unique `(device_id,scope)`; composite FK `(workspace_id,device_id)` | `(workspace_id,device_id)`; active-token partial index | workspace lifetime; secret hash retained until workspace deletion |
| `recovery_verifiers` | workspace primary key; unique `(workspace_id,locator)` | unique `(workspace_id,locator)` is the O(1) lookup | current verifier only; rotation overwrites atomically |
| `pairing_requests` | UUID primary key; unique short code; claim and invalidation terminal states mutually exclusive | pending expiry; approved/unclaimed/uninvalidated claim expiry; metadata purge cutoff | purge 24 hours after later expiry; unclaimed grant credentials/event are atomically revoked/recorded before `claim_invalidated_at` and purge |
| `workspace_events` | primary `(workspace_id,sequence)` | `(workspace_id,created_at)` and `expires_at` | 35 days |
| `idempotency_records` | primary `(scope_id,operation,key_hash)`; `scope_id` is `public` or canonical lowercase workspace UUID | `(scope_id,workspace_id,expires_at)` and `expires_at` | exactly 24 hours from database-assigned `created_at` |
| `rate_limit_buckets` | primary `(scope,subject_hash)` | `expires_at` | 24 hours after window close |

No row-level security policy is used in Phase 2. Isolation is enforced by composite foreign keys, repository signatures, and SQL predicates, then tested through both store and HTTP layers. Every established-workspace query starts from a supplied workspace UUID; no device UUID or token locator is globally dereferenced on its own. Authentication can do O(1) lookup because the bearer wire form supplies both non-secret workspace UUID and non-secret token locator before the secret hash is checked.

## Task 1: Pin dependencies and start local PostgreSQL

**Files:**

- Modify: `go.mod`
- Create: `go.sum`
- Create: `compose.yaml`

- [ ] **Step 1: Reconfirm the clean documentation baseline on current main**

Run:

```bash
test "$(git branch --show-current)" = "main"
test -z "$(git status --porcelain)"
test "$(git show -s --format=%s HEAD)" = "docs: record foundation and plan identity server"
git merge-base --is-ancestor 4084a5f HEAD
expected_docs=$'docs/superpowers/plans/2026-08-12-mcpaste-identity-server.md\ndocs/superpowers/records/2026-08-12-mcpaste-foundation.md'
test "$(git diff --name-only 4084a5f..HEAD | sort)" = "$expected_docs"
test "$(git diff-tree --no-commit-id --name-only -r HEAD | sort)" = "$expected_docs"
go version
go test -race ./cmd/server ./internal/config ./internal/httpserver
go vet ./cmd/server ./internal/config ./internal/httpserver
```

Expected: every Git assertion exits 0, proving a clean current `main`, exact current `HEAD` subject `docs: record foundation and plan identity server`, `4084a5f` as an ancestor, and one documentation-only baseline commit whose only two changed paths are the Foundation record and this Phase 2 plan. Go reports `go1.26.5`, and both Foundation Go checks pass. Do not require `HEAD` to equal `4084a5f`; stop if its subject differs or any other path differs between `4084a5f` and `HEAD`.

- [ ] **Step 2: Verify current primary module metadata without changing the module**

Run:

```bash
go list -m -json github.com/jackc/pgx/v5@v5.10.0
go list -m -json golang.org/x/crypto@v0.55.0
```

Expected: pgx reports version `v5.10.0`, origin hash `7293fb11125be0373a92f716683f2d494f6fd4b0`, and `GoVersion` 1.25.0; x/crypto reports version `v0.55.0`, origin hash `f44d03d253a1503e51b059ca880867c51d878242`, and `GoVersion` 1.25.0. Both are compatible with Go 1.26.5.

- [ ] **Step 3: Materialize the complete pruned module graph with only two direct requirements**

Replace `go.mod` with:

```go
module github.com/1yoouoo/mcpaste

go 1.26.0

require (
	github.com/jackc/pgx/v5 v5.10.0
	golang.org/x/crypto v0.55.0
)
```

Generate complete direct and transitive sums from the public checksum database before Task 2:

```bash
go get github.com/jackc/pgx/v5/pgxpool@v5.10.0 golang.org/x/crypto/argon2@v0.55.0
go mod download all
go mod verify
while read -r module version
do
  awk -v module="$module" -v version="$version" '
    $1 == module && $2 == version && $3 ~ /^h1:/ { found = 1 }
    END { exit(found ? 0 : 1) }
  ' go.sum
  test "$(go list -mod=readonly -m -f '{{.Version}}' "$module")" = "$version"
done <<'MODULES'
github.com/jackc/pgpassfile v1.0.0
github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761
github.com/jackc/puddle/v2 v2.2.2
golang.org/x/sync v0.22.0
golang.org/x/sys v0.47.0
golang.org/x/text v0.41.0
MODULES
diff -u - go.mod <<'EOF'
module github.com/1yoouoo/mcpaste

go 1.26.0

require (
	github.com/jackc/pgx/v5 v5.10.0
	golang.org/x/crypto v0.55.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)
EOF
test "$(awk '
  /^require \($/ { inside = 1; next }
  inside && /^\)$/ { inside = 0; next }
  inside && NF == 2 && $1 !~ /^\/\// { direct++ }
  END { print direct + 0 }
' go.mod)" = "2"
test "$(rg -c '// indirect$' go.mod)" = "6"
```

Expected: `go get` loads the two packages the phase imports, `go.sum` is created, and `go mod verify` prints `all modules verified`. The final `go.mod` is byte-for-byte the shown pruned graph: pgx and x/crypto are its only two direct requirements, and the six indirect requirements are exactly the modules needed by `pgxpool` and `argon2`. The verified Go 1.26.5 MVS result is `golang.org/x/sync v0.22.0`, not v0.17.0: `golang.org/x/text v0.41.0` itself requires v0.22.0, so forcing v0.17.0 is not a valid graph after `go mod download all`. Do not hand-edit `go.sum`.

- [ ] **Step 4: Prove the graph is readonly and compile-ready before Task 2**

Run:

```bash
module_graph_before="$(shasum -a 256 go.mod go.sum)"
dependency_packages="$(GOWORK=off go list -mod=readonly -deps \
  github.com/jackc/pgx/v5/pgxpool \
  golang.org/x/crypto/argon2)"
printf '%s\n' "$dependency_packages" | grep -Fx 'github.com/jackc/pgx/v5/pgxpool'
printf '%s\n' "$dependency_packages" | grep -Fx 'golang.org/x/crypto/argon2'
printf '%s\n' "$dependency_packages" | grep -Fx 'golang.org/x/text/secure/precis'
module_graph_after="$(shasum -a 256 go.mod go.sum)"
test "$module_graph_after" = "$module_graph_before"
unset dependency_packages module_graph_after module_graph_before
```

Expected: all three package checks print one exact line, `-mod=readonly` does not request a `go.mod` update, and both module-file digest lists are identical. This is the clean pre-Task-2 reproduction of the package graph used when Task 2 compiles its pgxpool imports.

- [ ] **Step 5: Create loopback-only local PostgreSQL**

Create `compose.yaml` with:

```yaml
services:
  postgres:
    image: postgres:18.4-alpine3.24@sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15
    environment:
      POSTGRES_DB: mcpaste
      POSTGRES_USER: mcpaste
      POSTGRES_PASSWORD: mcpaste-local-only-not-production
    ports:
      - "127.0.0.1:55439:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U mcpaste -d mcpaste"]
      interval: 2s
      timeout: 3s
      retries: 20
    volumes:
      - mcpaste-postgres:/var/lib/postgresql

volumes:
  mcpaste-postgres:
```

PostgreSQL 18 changed the official image volume target to `/var/lib/postgresql`; do not use the pre-18 `/var/lib/postgresql/data` mount.

- [ ] **Step 6: Verify the pinned image and database health**

Run:

```bash
docker buildx imagetools inspect postgres:18.4-alpine3.24
docker compose config --quiet
if ! command -v lsof >/dev/null 2>&1; then
  printf '%s\n' 'lsof is required for the read-only TCP port preflight.' >&2
  exit 1
fi
listener_status=0
listener_output="$(lsof -nP -iTCP:55439 -sTCP:LISTEN 2>&1)" || listener_status=$?
if test "$listener_status" -gt 1; then
  printf '%s\n' 'Unable to inspect TCP port 55439; stop without starting PostgreSQL.' >&2
  exit 1
fi
if test -n "$listener_output"; then
  printf '%s\n' 'TCP port 55439 is already in use. Stop here and identify its owner manually; do not stop or alter that process or container.' >&2
  printf '%s\n' "$listener_output" >&2
  exit 1
fi
unset listener_output listener_status
docker compose up -d --wait --wait-timeout 60 postgres
docker compose exec -T postgres pg_isready -U mcpaste -d mcpaste
docker compose exec -T postgres psql -U mcpaste -d mcpaste -Atc 'show server_version'
```

Expected: the image index digest is `sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15`; the read-only `lsof` preflight produces no listener on `127.0.0.1:55439`; any listener causes an immediate diagnostic and exit without stopping or altering its owner; Compose waits at most 60 seconds for the defined healthcheck; the post-wait assertion says `accepting connections`; and the server version begins `18.4`.

- [ ] **Step 7: Run dependency and whitespace checks**

```bash
go mod verify
go list -m all
git diff --check -- go.mod go.sum compose.yaml
```

Expected: checks pass; the module list contains pgx v5.10.0 and x/crypto v0.55.0; whitespace check prints nothing.

- [ ] **Step 8: Commit the dependency and local database boundary**

```bash
git add go.mod go.sum compose.yaml
git commit -m "build: add PostgreSQL identity dependencies"
```

Expected: one commit containing exactly those three files. Leave the local PostgreSQL service running for integration tasks.

## Task 2: Add the embedded migration runner

**Files:**

- Create: `db/migrations/embed.go`
- Create: `internal/database/migrate/migrate_test.go`
- Create: `internal/database/migrate/migrate.go`
- Create: `cmd/migrate/main.go`

- [ ] **Step 1: Write parser and checksum tests first**

Create `internal/database/migrate/migrate_test.go` with:

```go
package migrate

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
)

func TestLoadOrdersPairsAndChecksumsUpBytes(t *testing.T) {
	files := fstest.MapFS{
		"000002_second.up.sql":    {Data: []byte("select 2;\n")},
		"000001_first.down.sql":  {Data: []byte("select -1;\n")},
		"000001_first.up.sql":    {Data: []byte("select 1;\n")},
		"000002_second.down.sql": {Data: []byte("select -2;\n")},
	}

	got, err := Load(files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got) != 2 || got[0].Version != 1 || got[1].Version != 2 {
		t.Fatalf("versions = %#v", got)
	}
	if got[0].Name != "first" || got[0].Checksum != "4a45092ccf992ea92250053a80b931b787924ba61648f420555511b84f10ab6c" {
		t.Fatalf("first migration = %#v", got[0])
	}
}

func TestLoadRejectsInvalidSets(t *testing.T) {
	tests := map[string]struct {
		files fstest.MapFS
		want  string
	}{
		"missing down": {
			files: fstest.MapFS{
				"000001_first.up.sql": {Data: []byte("select 1")},
			},
			want: "migration version 000001 must have up and down files",
		},
		"gap": {
			files: fstest.MapFS{
				"000002_second.up.sql":   {Data: []byte("select 2")},
				"000002_second.down.sql": {Data: []byte("select -2")},
			},
			want: "migration sequence gap before version 000002",
		},
		"bad name": {
			files: fstest.MapFS{
				"1_first.up.sql": {Data: []byte("select 1")},
			},
			want: `invalid migration filename "1_first.up.sql"`,
		},
		"duplicate version with distinct basenames": {
			files: fstest.MapFS{
				"000001_first.up.sql":     {Data: []byte("select 1")},
				"000001_first.down.sql":   {Data: []byte("select -1")},
				"000001_another.up.sql":   {Data: []byte("select 2")},
				"000001_another.down.sql": {Data: []byte("select -2")},
			},
			want: "migration version 000001 has conflicting names",
		},
	}
	for name, item := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(item.files)
			if err == nil || err.Error() != item.want {
				t.Fatalf("Load() exact error match = %v", err != nil && err.Error() == item.want)
			}
		})
	}
}

func TestRequireCurrentRejectsZeroAvailableBeforeDatabaseAccess(t *testing.T) {
	_, err := RequireCurrent(context.Background(), nil, nil)
	if !errors.Is(err, ErrMigrationsNotCurrent) {
		t.Fatalf("RequireCurrent() error = %v", err)
	}
}
```

The checksum literal is the verified output of `printf 'select 1;\n' | shasum -a 256` for exactly `select 1;` plus newline.

- [ ] **Step 2: Run the focused test and observe the red state**

```bash
go test ./internal/database/migrate
```

Expected: FAIL because `Load` is undefined.

- [ ] **Step 3: Expose the embedded migration filesystem**

Create `db/migrations/embed.go` with:

```go
package migrations

import "embed"

// Files contains repository-owned ordered SQL migrations.
//
//go:embed *.sql
var Files embed.FS
```

The package will not compile until Task 3 creates the first SQL files; keep Task 2's focused command scoped to `internal/database/migrate` until then.

- [ ] **Step 4: Write the complete migration loader and runner**

Create `internal/database/migrate/migrate.go` with:

```go
package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const lockName = "mcpaste-schema-migrations-v1"

var ErrMigrationsNotCurrent = errors.New("database migrations are not current")

type Migration struct {
	Version  int64
	Name     string
	Checksum string
	Up       string
	Down     string
}

type Applied struct {
	Version  int64
	Name     string
	Checksum string
}

type MigrationStatus struct {
	Applied   []Applied
	Available int
}

type migrationQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func Load(source fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	type pair struct {
		name string
		up   []byte
		down []byte
	}
	pairs := make(map[int64]*pair)
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "embed.go" {
			continue
		}
		version, name, direction, err := parseName(entry.Name())
		if err != nil {
			return nil, err
		}
		body, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		current := pairs[version]
		if current == nil {
			current = &pair{name: name}
			pairs[version] = current
		}
		if current.name != name {
			return nil, fmt.Errorf("migration version %06d has conflicting names", version)
		}
		switch direction {
		case "up":
			if current.up != nil {
				return nil, fmt.Errorf("migration version %06d has duplicate up file", version)
			}
			current.up = body
		case "down":
			if current.down != nil {
				return nil, fmt.Errorf("migration version %06d has duplicate down file", version)
			}
			current.down = body
		}
	}
	versions := make([]int64, 0, len(pairs))
	for version := range pairs {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	result := make([]Migration, 0, len(versions))
	for index, version := range versions {
		if version != int64(index+1) {
			return nil, fmt.Errorf("migration sequence gap before version %06d", version)
		}
		current := pairs[version]
		if current.up == nil || current.down == nil {
			return nil, fmt.Errorf("migration version %06d must have up and down files", version)
		}
		sum := sha256.Sum256(current.up)
		result = append(result, Migration{
			Version:  version,
			Name:     current.name,
			Checksum: hex.EncodeToString(sum[:]),
			Up:       string(current.up),
			Down:     string(current.down),
		})
	}
	return result, nil
}

func parseName(value string) (int64, string, string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[2] != "sql" || (parts[1] != "up" && parts[1] != "down") {
		return 0, "", "", fmt.Errorf("invalid migration filename %q", value)
	}
	prefix := strings.SplitN(parts[0], "_", 2)
	if len(prefix) != 2 || len(prefix[0]) != 6 || prefix[1] == "" {
		return 0, "", "", fmt.Errorf("invalid migration filename %q", value)
	}
	version, err := strconv.ParseInt(prefix[0], 10, 64)
	if err != nil || version < 1 {
		return 0, "", "", fmt.Errorf("invalid migration filename %q", value)
	}
	for _, r := range prefix[1] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return 0, "", "", fmt.Errorf("invalid migration filename %q", value)
		}
	}
	return version, prefix[1], parts[1], nil
}

func WithLock(ctx context.Context, pool *pgxpool.Pool, fn func(*pgx.Conn) error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "select pg_advisory_lock(hashtextextended($1, 0))", lockName); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "select pg_advisory_unlock(hashtextextended($1, 0))", lockName)
	}()
	return fn(conn.Conn())
}

func EnsureTable(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
create table if not exists schema_migrations (
    version bigint primary key,
    name text not null,
    checksum char(64) not null check (checksum ~ '^[0-9a-f]{64}$'),
    applied_at timestamptz not null default transaction_timestamp()
)`)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	return nil
}

func Status(ctx context.Context, conn *pgx.Conn, available []Migration) (MigrationStatus, error) {
	if err := EnsureTable(ctx, conn); err != nil {
		return MigrationStatus{}, err
	}
	applied, err := readApplied(ctx, conn)
	if err != nil {
		return MigrationStatus{}, err
	}
	if err := Verify(available, applied); err != nil {
		return MigrationStatus{}, err
	}
	return MigrationStatus{Applied: applied, Available: len(available)}, nil
}

func CheckCurrent(ctx context.Context, queryer migrationQuerier, available []Migration) (MigrationStatus, error) {
	if len(available) == 0 {
		return MigrationStatus{}, ErrMigrationsNotCurrent
	}
	applied, err := readApplied(ctx, queryer)
	if err != nil {
		return MigrationStatus{}, err
	}
	if err := Verify(available, applied); err != nil {
		return MigrationStatus{}, err
	}
	status := MigrationStatus{Applied: applied, Available: len(available)}
	if len(status.Applied) != status.Available {
		return status, ErrMigrationsNotCurrent
	}
	return status, nil
}

func readApplied(ctx context.Context, queryer migrationQuerier) ([]Applied, error) {
	rows, err := queryer.Query(ctx, "select version, name, checksum from schema_migrations order by version")
	if err != nil {
		return nil, fmt.Errorf("query migration status: %w", err)
	}
	defer rows.Close()
	applied := make([]Applied, 0)
	for rows.Next() {
		var item Applied
		if err := rows.Scan(&item.Version, &item.Name, &item.Checksum); err != nil {
			return nil, fmt.Errorf("scan migration status: %w", err)
		}
		applied = append(applied, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration status: %w", err)
	}
	return applied, nil
}

func RequireCurrent(ctx context.Context, conn *pgx.Conn, available []Migration) (MigrationStatus, error) {
	if len(available) == 0 {
		return MigrationStatus{}, ErrMigrationsNotCurrent
	}
	status, err := Status(ctx, conn, available)
	if err != nil {
		return MigrationStatus{}, err
	}
	if len(status.Applied) != status.Available {
		return status, ErrMigrationsNotCurrent
	}
	return status, nil
}

func Verify(available []Migration, applied []Applied) error {
	if len(applied) > len(available) {
		return errors.New("database contains unknown migration versions")
	}
	for index, got := range applied {
		want := available[index]
		if got.Version != want.Version || got.Name != want.Name {
			return fmt.Errorf("database migration sequence differs at version %06d", got.Version)
		}
		if got.Checksum != want.Checksum {
			return fmt.Errorf("migration checksum mismatch at version %06d", got.Version)
		}
	}
	return nil
}

func Up(ctx context.Context, conn *pgx.Conn, available []Migration) error {
	status, err := Status(ctx, conn, available)
	if err != nil {
		return err
	}
	for _, migration := range available[len(status.Applied):] {
		if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, migration.Up); err != nil {
				return fmt.Errorf("apply migration %06d_%s: %w", migration.Version, migration.Name, err)
			}
			_, err := tx.Exec(ctx,
				"insert into schema_migrations(version, name, checksum) values ($1, $2, $3)",
				migration.Version, migration.Name, migration.Checksum,
			)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func DownOne(ctx context.Context, conn *pgx.Conn, available []Migration) error {
	status, err := Status(ctx, conn, available)
	if err != nil {
		return err
	}
	if len(status.Applied) == 0 {
		return errors.New("database has no applied migration")
	}
	migration := available[len(status.Applied)-1]
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, migration.Down); err != nil {
			return fmt.Errorf("roll back migration %06d_%s: %w", migration.Version, migration.Name, err)
		}
		_, err := tx.Exec(ctx, "delete from schema_migrations where version = $1", migration.Version)
		return err
	})
}
```

- [ ] **Step 5: Correct the checksum fixture and make the loader green**

Run:

```bash
gofmt -w internal/database/migrate/migrate.go internal/database/migrate/migrate_test.go db/migrations/embed.go
module_graph_before="$(shasum -a 256 go.mod go.sum)"
GOWORK=off go test -mod=readonly ./internal/database/migrate
module_graph_after="$(shasum -a 256 go.mod go.sum)"
test "$module_graph_after" = "$module_graph_before"
unset module_graph_after module_graph_before
```

Expected: PASS with the already verified checksum literal. The package compiles its pgx and pgxpool imports under `-mod=readonly`; it neither asks for a `go.mod` update nor changes `go.mod` or `go.sum`.

- [ ] **Step 6: Add the migration command with strict arguments**

Create `cmd/migrate/main.go` with:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/1yoouoo/mcpaste/db/migrations"
	"github.com/1yoouoo/mcpaste/internal/database/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mcpaste migrate: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string) error {
	if len(args) == 0 {
		return errors.New("usage: mcpaste-migrate up|status|verify|down --steps 1")
	}
	databaseURL := getenv("MCPASTE_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("MCPASTE_DATABASE_URL is required")
	}
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return errors.New("parse MCPASTE_DATABASE_URL")
	}
	defer pool.Close()
	return migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		switch args[0] {
		case "up":
			if len(args) != 1 {
				return errors.New("usage: mcpaste-migrate up")
			}
			return migrate.Up(ctx, conn, available)
		case "status":
			if len(args) != 1 {
				return errors.New("usage: mcpaste-migrate status")
			}
			status, err := migrate.Status(ctx, conn, available)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(os.Stdout, "applied=%d available=%d\n", len(status.Applied), status.Available)
			return nil
		case "verify":
			if len(args) != 1 {
				return errors.New("usage: mcpaste-migrate verify")
			}
			status, err := migrate.RequireCurrent(ctx, conn, available)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(os.Stdout, "applied=%d available=%d\n", len(status.Applied), status.Available)
			return nil
		case "down":
			if len(args) != 3 || args[1] != "--steps" || args[2] != "1" {
				return errors.New("usage: mcpaste-migrate down --steps 1")
			}
			status, err := migrate.Status(ctx, conn, available)
			if err != nil {
				return err
			}
			if len(status.Applied) == 0 {
				return errors.New("database has no applied migration")
			}
			selected := status.Applied[len(status.Applied)-1]
			_, _ = fmt.Fprintf(os.Stdout, "rolling_back=%06d_%s\n", selected.Version, selected.Name)
			return migrate.DownOne(ctx, conn, available)
		default:
			return errors.New("usage: mcpaste-migrate up|status|verify|down --steps 1")
		}
	})
}
```

- [ ] **Step 7: Verify the command fails only because SQL files do not exist yet**

```bash
gofmt -w cmd/migrate/main.go
go test ./internal/database/migrate
go test ./cmd/migrate
```

Expected: the migration package passes. The command package fails with the `go:embed` no-matching-files diagnostic, which Task 3 resolves; no other compile error is acceptable.

- [ ] **Step 8: Do not commit the intentionally incomplete build**

Task 2 and Task 3 form one compile-safe commit boundary. Continue directly to Task 3 with these files unstaged.

## Task 3: Create the Phase 2 schema and prove migration boundaries

**Files:**

- Create: `db/migrations/000001_identity.up.sql`
- Create: `db/migrations/000001_identity.down.sql`
- Create: `internal/testdb/testdb.go`
- Create: `internal/database/migrate/schema_integration_test.go`
- Create: `internal/database/migrate/migrate_integration_test.go`

- [ ] **Step 1: Write the schema contract integration test before the migration exists**

Create `internal/database/migrate/schema_integration_test.go` with:

```go
package migrate_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/db/migrations"
	"github.com/1yoouoo/mcpaste/internal/database/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIdentitySchemaContract(t *testing.T) {
	databaseURL := os.Getenv("MCPASTE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MCPASTE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal("parse test database URL")
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		t.Fatal("connect to test database")
	}

	schema := fmt.Sprintf("mcpaste_schema_contract_%d_%d", os.Getpid(), time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "create schema "+identifier); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	defer func() {
		_, _ = admin.Exec(context.Background(), "drop schema "+identifier+" cascade")
	}()

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("parse isolated database URL")
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("open isolated database pool")
	}
	defer pool.Close()

	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		return migrate.Up(ctx, conn, available)
	}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	wantTables := []string{
		"credentials",
		"devices",
		"idempotency_records",
		"pairing_requests",
		"rate_limit_buckets",
		"recovery_verifiers",
		"workspace_events",
		"workspaces",
	}
	for _, table := range wantTables {
		var count int
		if err := pool.QueryRow(ctx, `
select count(*)
from information_schema.tables
where table_schema = $1 and table_name = $2`, schema, table).Scan(&count); err != nil {
			t.Fatalf("inspect table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}

	var claimInvalidatedType string
	if err := pool.QueryRow(ctx, `
select data_type
from information_schema.columns
where table_schema = $1
  and table_name = 'pairing_requests'
  and column_name = 'claim_invalidated_at'`, schema).Scan(&claimInvalidatedType); err != nil {
		t.Fatalf("inspect claim_invalidated_at: %v", err)
	}
	if claimInvalidatedType != "timestamp with time zone" {
		t.Fatalf("claim_invalidated_at type = %q", claimInvalidatedType)
	}

	var idempotencyPrimaryKey string
	if err := pool.QueryRow(ctx, `
select pg_get_constraintdef(oid)
from pg_constraint
where conrelid = to_regclass($1) and contype = 'p'`, schema+".idempotency_records").Scan(&idempotencyPrimaryKey); err != nil {
		t.Fatalf("inspect idempotency primary key: %v", err)
	}
	if idempotencyPrimaryKey != "PRIMARY KEY (scope_id, operation, key_hash)" {
		t.Fatalf("idempotency primary key = %q", idempotencyPrimaryKey)
	}
	var createdAtDefault *string
	if err := pool.QueryRow(ctx, `
select column_default
from information_schema.columns
where table_schema = $1
  and table_name = 'idempotency_records'
  and column_name = 'created_at'`, schema).Scan(&createdAtDefault); err != nil {
		t.Fatalf("inspect idempotency created_at: %v", err)
	}
	if createdAtDefault != nil {
		t.Fatal("idempotency created_at has an unexpected default")
	}

	for _, index := range []string{
		"credentials_active_lookup_index",
		"devices_workspace_display_name_ci_unique",
		"idempotency_expiry_index",
		"idempotency_scope_workspace_expiry_index",
		"pairing_claim_expiry_index",
		"pairing_metadata_purge_index",
		"rate_limit_expiry_index",
		"workspace_events_expiry_index",
	} {
		var count int
		if err := pool.QueryRow(ctx, `
select count(*)
from pg_indexes
where schemaname = $1 and indexname = $2`, schema, index).Scan(&count); err != nil {
			t.Fatalf("inspect index %s: %v", index, err)
		}
		if count != 1 {
			t.Fatalf("index %s count = %d, want 1", index, count)
		}
	}
}
```

- [ ] **Step 2: Run the schema test red**

```bash
gofmt -w internal/database/migrate/schema_integration_test.go
export MCPASTE_TEST_DATABASE_URL='postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable'
go test -race ./internal/database/migrate -run TestIdentitySchemaContract -count=1
```

Expected: FAIL before the SQL files exist with `pattern *.sql: no matching files found`. This is the intended red state; any Go syntax or type error is not acceptable.

- [ ] **Step 3: Write the complete expand migration**

Create `db/migrations/000001_identity.up.sql` with no `BEGIN` or `COMMIT` because the runner owns the transaction:

```sql
create table workspaces (
    id uuid primary key default gen_random_uuid(),
    next_event_sequence bigint not null default 0 check (next_event_sequence >= 0),
    created_at timestamptz not null default transaction_timestamp()
);

create table devices (
    id uuid primary key default gen_random_uuid(),
    workspace_id uuid not null references workspaces(id) on delete cascade,
    display_name varchar(80) not null,
    platform text not null check (platform in ('macos', 'linux')),
    role text not null check (role in ('full', 'connector')),
    created_at timestamptz not null default transaction_timestamp(),
    revoked_at timestamptz,
    constraint devices_full_is_macos check (role <> 'full' or platform = 'macos'),
    constraint devices_display_name_trimmed check (display_name = btrim(display_name)),
    constraint devices_display_name_nonempty check (char_length(display_name) between 1 and 80),
    constraint devices_workspace_id_id_unique unique (workspace_id, id)
);

create unique index devices_workspace_display_name_ci_unique
    on devices (workspace_id, lower(display_name));
create index devices_workspace_created_index
    on devices (workspace_id, created_at, id);
create index devices_workspace_revoked_index
    on devices (workspace_id, revoked_at);

create table credentials (
    id uuid primary key default gen_random_uuid(),
    workspace_id uuid not null,
    device_id uuid not null,
    token_id varchar(22) not null,
    scope text not null check (scope in ('full', 'connector')),
    secret_hash bytea not null check (octet_length(secret_hash) = 32),
    created_at timestamptz not null default transaction_timestamp(),
    last_used_at timestamptz,
    revoked_at timestamptz,
    constraint credentials_workspace_device_fk
        foreign key (workspace_id, device_id)
        references devices(workspace_id, id) on delete cascade,
    constraint credentials_workspace_token_unique unique (workspace_id, token_id),
    constraint credentials_device_scope_unique unique (device_id, scope),
    constraint credentials_token_id_shape check (token_id ~ '^[A-Za-z0-9_-]{22}$')
);

create index credentials_workspace_device_index
    on credentials (workspace_id, device_id);
create index credentials_active_lookup_index
    on credentials (workspace_id, token_id)
    where revoked_at is null;

create table recovery_verifiers (
    workspace_id uuid primary key references workspaces(id) on delete cascade,
    locator varchar(22) not null,
    salt bytea not null check (octet_length(salt) = 16),
    verifier bytea not null check (octet_length(verifier) = 32),
    argon_version smallint not null check (argon_version = 19),
    argon_time integer not null check (argon_time = 3),
    argon_memory_kib integer not null check (argon_memory_kib = 65536),
    argon_threads smallint not null check (argon_threads = 4),
    created_at timestamptz not null default transaction_timestamp(),
    rotated_at timestamptz not null default transaction_timestamp(),
    constraint recovery_workspace_locator_unique unique (workspace_id, locator),
    constraint recovery_locator_shape check (locator ~ '^[A-Za-z0-9_-]{22}$')
);

create table pairing_requests (
    id uuid primary key default gen_random_uuid(),
    short_code char(8) not null unique,
    claim_hash bytea not null check (octet_length(claim_hash) = 32),
    proposed_name varchar(80) not null,
    platform text not null check (platform in ('macos', 'linux')),
    requested_scope text not null check (requested_scope in ('full', 'connector')),
    workspace_id uuid references workspaces(id) on delete cascade,
    approved_by_device_id uuid,
    device_id uuid,
    created_at timestamptz not null default transaction_timestamp(),
    expires_at timestamptz not null,
    approved_at timestamptz,
    claim_expires_at timestamptz,
    claimed_at timestamptz,
    claim_invalidated_at timestamptz,
    grant_key_id varchar(32),
    grant_nonce bytea,
    grant_ciphertext bytea,
    metadata_purge_at timestamptz not null,
    constraint pairing_short_code_shape check (
        short_code ~ '^[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{8}$'
    ),
    constraint pairing_full_is_macos check (requested_scope <> 'full' or platform = 'macos'),
    constraint pairing_name_trimmed check (proposed_name = btrim(proposed_name)),
    constraint pairing_name_nonempty check (char_length(proposed_name) between 1 and 80),
    constraint pairing_expiry_after_create check (expires_at > created_at),
    constraint pairing_workspace_approver_fk
        foreign key (workspace_id, approved_by_device_id)
        references devices(workspace_id, id),
    constraint pairing_workspace_device_fk
        foreign key (workspace_id, device_id)
        references devices(workspace_id, id),
    constraint pairing_approval_fields_together check (
        (workspace_id is null and approved_by_device_id is null and device_id is null and
         approved_at is null and claim_expires_at is null and grant_key_id is null and
         grant_nonce is null and grant_ciphertext is null)
        or
        (workspace_id is not null and approved_by_device_id is not null and device_id is not null and
         approved_at is not null and claim_expires_at is not null and grant_key_id is not null and
         grant_nonce is not null and grant_ciphertext is not null)
    ),
    constraint pairing_grant_key_id_shape check (
        grant_key_id is null or grant_key_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$'
    ),
    constraint pairing_grant_nonce_size check (
        grant_nonce is null or octet_length(grant_nonce) = 12
    ),
    constraint pairing_claim_after_approval check (
        claimed_at is null or (approved_at is not null and claimed_at >= approved_at)
    ),
    constraint pairing_claim_terminal_exclusive check (
        claimed_at is null or claim_invalidated_at is null
    ),
    constraint pairing_invalidation_after_approval check (
        claim_invalidated_at is null or
        (approved_at is not null and claim_invalidated_at >= approved_at)
    )
);

create index pairing_pending_expiry_index
    on pairing_requests (expires_at)
    where approved_at is null;
create index pairing_claim_expiry_index
    on pairing_requests (claim_expires_at)
    where approved_at is not null and claimed_at is null and claim_invalidated_at is null;
create index pairing_metadata_purge_index
    on pairing_requests (metadata_purge_at);

create table workspace_events (
    workspace_id uuid not null references workspaces(id) on delete cascade,
    sequence bigint not null check (sequence > 0),
    event_type text not null check (event_type in (
        'device.added', 'device.renamed', 'device.revoked', 'recovery.rotated'
    )),
    object_id uuid not null,
    metadata jsonb not null default '{}'::jsonb check (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz not null default transaction_timestamp(),
    expires_at timestamptz not null,
    primary key (workspace_id, sequence)
);

create index workspace_events_created_index
    on workspace_events (workspace_id, created_at);
create index workspace_events_expiry_index
    on workspace_events (expires_at);

create table idempotency_records (
    scope_id varchar(36) not null,
    operation varchar(64) not null,
    key_hash bytea not null check (octet_length(key_hash) = 32),
    workspace_id uuid references workspaces(id) on delete cascade,
    request_hash bytea not null check (octet_length(request_hash) = 32),
    response_status smallint not null check (response_status between 200 and 299),
    response_content_type text not null check (response_content_type = 'application/json'),
    response_key_id varchar(32) not null check (
        response_key_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$'
    ),
    response_nonce bytea not null check (octet_length(response_nonce) = 12),
    response_ciphertext bytea not null,
    created_at timestamptz not null,
    expires_at timestamptz not null,
    primary key (scope_id, operation, key_hash),
    constraint idempotency_scope_shape check (
        scope_id = 'public' or
        scope_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
    ),
    constraint idempotency_exact_lifetime check (expires_at = created_at + interval '24 hours')
);

create index idempotency_scope_workspace_expiry_index
    on idempotency_records (scope_id, workspace_id, expires_at);
create index idempotency_expiry_index
    on idempotency_records (expires_at);

create table rate_limit_buckets (
    scope varchar(64) not null,
    subject_hash bytea not null check (octet_length(subject_hash) = 32),
    window_started_at timestamptz not null,
    request_count integer not null check (request_count > 0),
    expires_at timestamptz not null,
    primary key (scope, subject_hash),
    constraint rate_limit_expiry_after_window check (expires_at > window_started_at)
);

create index rate_limit_expiry_index
    on rate_limit_buckets (expires_at);
```

- [ ] **Step 4: Write the exact local rollback migration**

Create `db/migrations/000001_identity.down.sql` with:

```sql
drop table if exists rate_limit_buckets;
drop table if exists idempotency_records;
drop table if exists workspace_events;
drop table if exists pairing_requests;
drop table if exists recovery_verifiers;
drop table if exists credentials;
drop table if exists devices;
drop table if exists workspaces;
```

- [ ] **Step 5: Run the schema contract test green**

```bash
go test -race ./internal/database/migrate -run TestIdentitySchemaContract -count=1
```

Expected: PASS; the migration creates all eight Phase 2 tables, the claim-invalidation column, and every named security or retention index in an isolated schema.

- [ ] **Step 6: Create the isolated integration database helper**

Create `internal/testdb/testdb.go` with:

```go
package testdb

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/1yoouoo/mcpaste/db/migrations"
	"github.com/1yoouoo/mcpaste/internal/database/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var schemaCounter atomic.Uint64

func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return open(t, true)
}

func NewUnmigrated(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return open(t, false)
}

func open(t *testing.T, apply bool) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("MCPASTE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MCPASTE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal("parse test database URL")
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Fatal("connect to test database")
	}
	schema := fmt.Sprintf("mcpaste_test_%d_%d", os.Getpid(), schemaCounter.Add(1))
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "create schema "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create isolated schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal("parse isolated database URL")
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatal("open isolated database pool")
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "drop schema "+identifier+" cascade")
		admin.Close()
	})
	if apply {
		available, err := migrate.Load(migrations.Files)
		if err != nil {
			t.Fatalf("load migrations: %v", err)
		}
		if err := migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
			return migrate.Up(ctx, conn, available)
		}); err != nil {
			t.Fatalf("apply migrations: %v", err)
		}
	}
	return pool
}
```

- [ ] **Step 7: Write migration lifecycle coverage**

Create `internal/database/migrate/migrate_integration_test.go` with:

```go
package migrate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/1yoouoo/mcpaste/db/migrations"
	"github.com/1yoouoo/mcpaste/internal/database/migrate"
	"github.com/1yoouoo/mcpaste/internal/testdb"
	"github.com/jackc/pgx/v5"
)

func TestStatusReportsPartialAndRequireCurrentRejectsIt(t *testing.T) {
	ctx := context.Background()
	pool := testdb.NewUnmigrated(t)
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	err = migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		status, err := migrate.Status(ctx, conn, available)
		if err != nil {
			return err
		}
		if len(status.Applied) != 0 || status.Available != 1 {
			t.Fatalf("partial status counts = %d/%d", len(status.Applied), status.Available)
		}
		if _, err := migrate.RequireCurrent(ctx, conn, available); !errors.Is(err, migrate.ErrMigrationsNotCurrent) {
			t.Fatalf("RequireCurrent() partial error = %v", err)
		}
		if err := migrate.Up(ctx, conn, available); err != nil {
			return err
		}
		current, err := migrate.RequireCurrent(ctx, conn, available)
		if err != nil {
			return err
		}
		if len(current.Applied) != 1 || current.Available != 1 {
			t.Fatalf("current status counts = %d/%d", len(current.Applied), current.Available)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("migration status lifecycle: %v", err)
	}
}

func TestUpStatusDownAndReapply(t *testing.T) {
	ctx := context.Background()
	pool := testdb.NewUnmigrated(t)
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	err = migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		if err := migrate.Up(ctx, conn, available); err != nil {
			return err
		}
		status, err := migrate.Status(ctx, conn, available)
		if err != nil {
			return err
		}
		if len(status.Applied) != 1 || status.Available != 1 || status.Applied[0].Name != "identity" {
			t.Fatalf("status counts/name = %d/%d/%q", len(status.Applied), status.Available, status.Applied[0].Name)
		}
		if err := migrate.DownOne(ctx, conn, available); err != nil {
			return err
		}
		var tableName *string
		if err := conn.QueryRow(ctx, "select to_regclass('workspaces')::text").Scan(&tableName); err != nil {
			return err
		}
		if tableName != nil {
			t.Fatalf("workspaces still exists: %q", *tableName)
		}
		return migrate.Up(ctx, conn, available)
	})
	if err != nil {
		t.Fatalf("migration lifecycle: %v", err)
	}
}

func TestVerifyRejectsChangedAppliedChecksum(t *testing.T) {
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	applied := []migrate.Applied{{
		Version:  available[0].Version,
		Name:     available[0].Name,
		Checksum: strings.Repeat("0", 64),
	}}
	if err := migrate.Verify(available, applied); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestCheckCurrentSucceedsInsideReadOnlyTransaction(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal("acquire read-only test connection")
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal("begin read-only transaction")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	status, err := migrate.CheckCurrent(ctx, tx, available)
	if err != nil {
		t.Fatalf("CheckCurrent() error = %v", err)
	}
	if len(status.Applied) != status.Available || status.Available != 1 {
		t.Fatalf("read-only current counts = %d/%d", len(status.Applied), status.Available)
	}
}

func TestRequireCurrentRejectsUnknownVersionAndChecksumDrift(t *testing.T) {
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	tests := []struct {
		name      string
		statement string
		argument1 any
		argument2 any
		wantKind  string
	}{
		{
			name: "unknown applied version",
			statement: `
insert into schema_migrations(version, name, checksum)
values ($1, 'unknown', $2)`,
			argument1: available[len(available)-1].Version + 1,
			argument2: strings.Repeat("0", 64),
			wantKind:  "unknown migration versions",
		},
		{
			name:      "checksum drift",
			statement: "update schema_migrations set checksum = $1 where version = $2",
			argument1: strings.Repeat("0", 64),
			argument2: available[0].Version,
			wantKind:  "checksum mismatch",
		},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			ctx := context.Background()
			pool := testdb.New(t)
			if _, err := pool.Exec(ctx, item.statement, item.argument1, item.argument2); err != nil {
				t.Fatal("mutate isolated migration state")
			}
			err := migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
				_, err := migrate.RequireCurrent(ctx, conn, available)
				return err
			})
			if err == nil || !strings.Contains(err.Error(), item.wantKind) {
				t.Fatalf("RequireCurrent rejection metadata: nil=%v expected_kind=%v", err == nil, err != nil && strings.Contains(err.Error(), item.wantKind))
			}
		})
	}
}
```

- [ ] **Step 8: Run the migration lifecycle against PostgreSQL**

```bash
gofmt -w db/migrations/embed.go internal/testdb/testdb.go internal/database/migrate/schema_integration_test.go internal/database/migrate/migrate_integration_test.go cmd/migrate/main.go
export MCPASTE_TEST_DATABASE_URL='postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable'
export MCPASTE_DATABASE_URL="$MCPASTE_TEST_DATABASE_URL"
go test -race ./internal/database/migrate -run 'TestCheckCurrentSucceedsInsideReadOnlyTransaction|TestRequireCurrentRejectsUnknownVersionAndChecksumDrift' -count=1
go test -race ./internal/database/migrate
go test ./cmd/migrate
go run ./cmd/migrate up
go run ./cmd/migrate status
go run ./cmd/migrate verify
```

Expected: tests pass; `TestCheckCurrentSucceedsInsideReadOnlyTransaction` proves the lock-free currentness path performs no schema write; command packages compile; status and verify print `applied=1 available=1`.

- [ ] **Step 9: Prove checksum and rollback boundaries manually**

```bash
go run ./cmd/migrate down --steps 2
go run ./cmd/migrate down --steps 1
go run ./cmd/migrate status
if go run ./cmd/migrate verify; then
  exit 1
fi
go run ./cmd/migrate up
go run ./cmd/migrate verify
```

Expected: the first command fails with the exact usage line and changes nothing; the one-step down succeeds; status prints `applied=0 available=1` and exits 0; verify fails with `database migrations are not current`; re-up and final verify succeed.

- [ ] **Step 10: Check schema constraints directly**

```bash
docker compose exec -T postgres psql -U mcpaste -d mcpaste -Atc "select tablename from pg_tables where schemaname='public' order by tablename"
docker compose exec -T postgres psql -U mcpaste -d mcpaste -Atc "select indexname from pg_indexes where schemaname='public' order by indexname"
```

Expected: the table list includes all eight Phase 2 tables plus `schema_migrations`; index output includes case-insensitive device names, active credential lookup, pairing expiry, event expiry, idempotency expiry, and rate-limit expiry indexes. It contains no paste, text, image, MCP, or session table.

- [ ] **Step 11: Commit the migration unit**

```bash
git add db/migrations internal/database/migrate internal/testdb cmd/migrate
git commit -m "feat: add identity schema migrations"
```

Expected: one compile-safe commit containing the runner, command, schema, rollback, and migration tests.

## Task 4: Build the cryptographic envelope and secret formats

**Files:**

- Create: `internal/secure/random.go`
- Create: `internal/secure/base64.go`
- Create: `internal/secure/argon2.go`
- Create: `internal/secure/argon2_test.go`
- Create: `internal/secure/envelope.go`
- Create: `internal/secure/envelope_test.go`
- Create: `internal/secure/credential.go`
- Create: `internal/secure/credential_test.go`
- Create: `internal/secure/recovery.go`
- Create: `internal/secure/recovery_test.go`

- [ ] **Step 1: Write the AES envelope tests**

Create `internal/secure/envelope_test.go` with:

```go
package secure

import (
	"bytes"
	"strings"
	"testing"
)

func TestEnvelopeRoundTripAndNonceUniqueness(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, 32)
	nonces := bytes.Repeat([]byte{0x41}, 24)
	copy(nonces[12:], bytes.Repeat([]byte{0x42}, 12))
	keyring, err := NewKeyring("test-key", map[string][]byte{"test-key": key}, bytes.NewReader(nonces))
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	first, err := keyring.Encrypt("idempotency", "object-1", []byte("sensitive-marker"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	second, err := keyring.Encrypt("idempotency", "object-1", []byte("sensitive-marker"))
	if err != nil {
		t.Fatalf("Encrypt() second error = %v", err)
	}
	if bytes.Equal(first.Nonce, second.Nonce) {
		t.Fatal("nonces are equal")
	}
	plaintext, err := keyring.Decrypt("idempotency", "object-1", first)
	if err != nil || string(plaintext) != "sensitive-marker" {
		t.Fatalf("Decrypt() = %q, %v", plaintext, err)
	}
}

func TestEnvelopeRejectsTamperWrongContextAndUnknownKey(t *testing.T) {
	keyring, err := NewKeyring(
		"test-key",
		map[string][]byte{
			"test-key":  bytes.Repeat([]byte{0x51}, 32),
			"other-key": bytes.Repeat([]byte{0x52}, 32),
		},
		bytes.NewReader(bytes.Repeat([]byte{0x61}, 36)),
	)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	envelope, err := keyring.Encrypt("pairing-grant", "pair-1", []byte("grant-marker"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	tampered := envelope
	tampered.Ciphertext = bytes.Clone(envelope.Ciphertext)
	tampered.Ciphertext[0] ^= 0xff
	if _, err := keyring.Decrypt("pairing-grant", "pair-1", tampered); err == nil {
		t.Fatal("tampered ciphertext decrypted")
	}
	if _, err := keyring.Decrypt("pairing-grant", "pair-2", envelope); err == nil {
		t.Fatal("wrong associated data decrypted")
	}
	knownIDTamper := envelope
	knownIDTamper.KeyID = "other-key"
	if _, err := keyring.Decrypt("pairing-grant", "pair-1", knownIDTamper); err == nil {
		t.Fatal("known key identifier tamper decrypted")
	}
	unknownIDTamper := envelope
	unknownIDTamper.KeyID = "missing-key"
	if _, err := keyring.Decrypt("pairing-grant", "pair-1", unknownIDTamper); err == nil {
		t.Fatal("unknown key identifier decrypted")
	}
}

func TestParseKeyringRejectsInvalidConfiguration(t *testing.T) {
	canonical := strings.Repeat("A", 43)
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "bad identifier", value: "bad id:YWJj"},
		{name: "invalid base64", value: "key:not-base64"},
		{name: "short key", value: "key:YWJj"},
		{name: "padding", value: "key:" + canonical + "="},
		{name: "noncanonical alias", value: "key:" + strings.Repeat("A", 42) + "B"},
		{name: "carriage return", value: "key:" + canonical[:21] + "\r" + canonical[21:]},
		{name: "line feed", value: "key:" + canonical[:21] + "\n" + canonical[21:]},
		{name: "duplicate identifier", value: "key:" + canonical + ",key:" + canonical},
		{name: "duplicate material", value: "key:" + canonical + ",other:" + canonical},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			if _, err := ParseKeyring("key", item.value, bytes.NewReader(nil)); err == nil {
				t.Fatal("ParseKeyring() error = nil")
			}
		})
	}
	if _, err := NewKeyring("first", map[string][]byte{
		"first":  bytes.Repeat([]byte{0x71}, 32),
		"second": bytes.Repeat([]byte{0x71}, 32),
	}, bytes.NewReader(nil)); err == nil {
		t.Fatal("NewKeyring() accepted duplicate key material")
	}
}

func TestEnvelopeRotationRetainsOldKeyAndUsesNewActiveKey(t *testing.T) {
	oldKey := bytes.Repeat([]byte{0x81}, 32)
	newKey := bytes.Repeat([]byte{0x82}, 32)
	oldRing, err := NewKeyring("old-key", map[string][]byte{"old-key": oldKey}, bytes.NewReader(bytes.Repeat([]byte{0x83}, 12)))
	if err != nil {
		t.Fatalf("NewKeyring() old error = %v", err)
	}
	oldEnvelope, err := oldRing.Encrypt("idempotency", "rotation-object", []byte("old-marker"))
	if err != nil {
		t.Fatalf("Encrypt() old error = %v", err)
	}
	rotatedRing, err := NewKeyring(
		"new-key",
		map[string][]byte{"old-key": oldKey, "new-key": newKey},
		bytes.NewReader(bytes.Repeat([]byte{0x84}, 12)),
	)
	if err != nil {
		t.Fatalf("NewKeyring() rotated error = %v", err)
	}
	if plaintext, err := rotatedRing.Decrypt("idempotency", "rotation-object", oldEnvelope); err != nil || string(plaintext) != "old-marker" {
		t.Fatal("rotated keyring could not decrypt retained old envelope")
	}
	newEnvelope, err := rotatedRing.Encrypt("idempotency", "rotation-object", []byte("new-marker"))
	if err != nil {
		t.Fatalf("Encrypt() new error = %v", err)
	}
	if newEnvelope.KeyID != "new-key" {
		t.Fatalf("new envelope key ID = %q", newEnvelope.KeyID)
	}
	if _, err := oldRing.Decrypt("idempotency", "rotation-object", newEnvelope); err == nil {
		t.Fatal("old-only keyring decrypted new envelope")
	}
}
```

- [ ] **Step 2: Run the envelope tests red**

```bash
go test ./internal/secure -run 'TestEnvelope|TestParseKeyring'
```

Expected: FAIL because the package and constructors are undefined.

- [ ] **Step 3: Create the explicit production/test randomness seam**

Create `internal/secure/random.go` with:

```go
package secure

import (
	"crypto/rand"
	"io"
)

type Random interface {
	Read([]byte) (int, error)
}

type SystemRandom struct{}

func (SystemRandom) Read(target []byte) (int, error) {
	return rand.Read(target)
}

func randomBytes(source Random, size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(source, value); err != nil {
		return nil, err
	}
	return value, nil
}
```

- [ ] **Step 4: Write the complete AES-256-GCM keyring**

Create `internal/secure/base64.go` with:

```go
package secure

import (
	"encoding/base64"
	"errors"
)

var errInvalidRawURL = errors.New("raw URL-base64 value is invalid")

func decodeCanonicalRawURL(value string, expectedBytes int) ([]byte, error) {
	if expectedBytes < 0 || len(value) != base64.RawURLEncoding.EncodedLen(expectedBytes) {
		return nil, errInvalidRawURL
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != expectedBytes {
		return nil, errInvalidRawURL
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errInvalidRawURL
	}
	return decoded, nil
}
```

Create `internal/secure/envelope.go` with:

```go
package secure

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"strings"
)

const nonceSize = 12

type Envelope struct {
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
}

type Keyring struct {
	active string
	keys   map[string][]byte
	random Random
}

func ParseKeyring(active, encoded string, random Random) (*Keyring, error) {
	if encoded == "" {
		return nil, errors.New("encryption keyring is empty")
	}
	keys := make(map[string][]byte)
	for _, item := range strings.Split(encoded, ",") {
		parts := strings.SplitN(item, ":", 2)
		if len(parts) != 2 || !validKeyID(parts[0]) {
			return nil, errors.New("encryption keyring entry is invalid")
		}
		if _, exists := keys[parts[0]]; exists {
			return nil, errors.New("encryption key identifier is duplicated")
		}
		decoded, err := decodeCanonicalRawURL(parts[1], 32)
		if err != nil {
			return nil, errors.New("encryption key must be 32 raw URL-base64 bytes")
		}
		for _, existing := range keys {
			if bytes.Equal(existing, decoded) {
				return nil, errors.New("encryption key material is duplicated")
			}
		}
		keys[parts[0]] = decoded
	}
	return NewKeyring(active, keys, random)
}

func NewKeyring(active string, keys map[string][]byte, random Random) (*Keyring, error) {
	if !validKeyID(active) || random == nil {
		return nil, errors.New("active key identifier or random source is invalid")
	}
	copied := make(map[string][]byte, len(keys))
	for id, key := range keys {
		if !validKeyID(id) || len(key) != 32 {
			return nil, errors.New("keyring contains an invalid key")
		}
		for _, existing := range copied {
			if bytes.Equal(existing, key) {
				return nil, errors.New("keyring contains duplicate key material")
			}
		}
		copied[id] = bytes.Clone(key)
	}
	if _, ok := copied[active]; !ok {
		return nil, errors.New("active key identifier is absent from keyring")
	}
	return &Keyring{active: active, keys: copied, random: random}, nil
}

func (k *Keyring) Encrypt(purpose, objectID string, plaintext []byte) (Envelope, error) {
	aead, err := newGCM(k.keys[k.active])
	if err != nil {
		return Envelope{}, err
	}
	nonce, err := randomBytes(k.random, nonceSize)
	if err != nil {
		return Envelope{}, fmt.Errorf("generate envelope nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, associatedData(k.active, purpose, objectID))
	return Envelope{KeyID: k.active, Nonce: nonce, Ciphertext: ciphertext}, nil
}

func (k *Keyring) Decrypt(purpose, objectID string, envelope Envelope) ([]byte, error) {
	key, ok := k.keys[envelope.KeyID]
	if !ok || len(envelope.Nonce) != nonceSize {
		return nil, errors.New("encrypted envelope is invalid")
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, errors.New("encrypted envelope is invalid")
	}
	plaintext, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, associatedData(envelope.KeyID, purpose, objectID))
	if err != nil {
		return nil, errors.New("encrypted envelope authentication failed")
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func associatedData(keyID, purpose, objectID string) []byte {
	return []byte("mcpaste:v1:" + keyID + ":" + purpose + ":" + objectID)
}

func validKeyID(value string) bool {
	if len(value) < 1 || len(value) > 32 {
		return false
	}
	for index, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (index > 0 && (r == '_' || r == '-')) {
			continue
		}
		return false
	}
	return true
}
```

- [ ] **Step 5: Make the envelope tests green**

```bash
gofmt -w internal/secure/random.go internal/secure/base64.go internal/secure/envelope.go internal/secure/envelope_test.go
go test -race ./internal/secure -run 'TestEnvelope|TestParseKeyring'
```

Expected: PASS, proving two encryptions use distinct nonces; tamper, context mismatch, and wrong key ID fail closed; duplicate key bytes are rejected; and key material rejects wrong encoded length, padding, non-zero trailing bits, CR, and LF through the shared canonical decoder.

- [ ] **Step 6: Write bearer and claim-secret tests**

Create `internal/secure/credential_test.go` with:

```go
package secure

import (
	"bytes"
	"crypto/subtle"
	"strings"
	"testing"
)

const testWorkspaceID = "00000000-0000-4000-8000-000000000001"

func TestCredentialCarriesLocatorAndHashesSecret(t *testing.T) {
	randomInput := make([]byte, 48)
	copy(randomInput[:16], bytes.Repeat([]byte{0x11}, 16))
	copy(randomInput[16:], bytes.Repeat([]byte{0x22}, 32))
	source := bytes.NewReader(randomInput)
	issued, err := NewCredential(testWorkspaceID, "full", source)
	if err != nil {
		t.Fatalf("NewCredential() error = %v", err)
	}
	if len(issued.Locator) != 22 || len(issued.Hash) != 32 || len(issued.Token) != 108 {
		t.Fatalf("issued lengths = locator %d hash %d token %d", len(issued.Locator), len(issued.Hash), len(issued.Token))
	}
	parsed, err := ParseCredential(issued.Token)
	if err != nil {
		t.Fatalf("ParseCredential() error = %v", err)
	}
	if parsed.WorkspaceID != testWorkspaceID || parsed.Locator != issued.Locator || subtle.ConstantTimeCompare(parsed.Hash, issued.Hash) != 1 {
		t.Fatalf("parsed credential metadata differs")
	}
}

func TestCredentialRejectsMalformedValues(t *testing.T) {
	canonicalLocator := strings.Repeat("A", 22)
	canonicalSecret := strings.Repeat("A", 43)
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "segments", value: "mcp1"},
		{name: "prefix", value: "mcp2.a.b.c"},
		{name: "workspace", value: "mcp1.bad-uuid.a.b"},
		{name: "short segments", value: "mcp1." + testWorkspaceID + ".short.short"},
		{name: "locator padding", value: "mcp1." + testWorkspaceID + "." + canonicalLocator + "=." + canonicalSecret},
		{name: "secret padding", value: "mcp1." + testWorkspaceID + "." + canonicalLocator + "." + canonicalSecret + "="},
		{name: "locator alias", value: "mcp1." + testWorkspaceID + "." + strings.Repeat("A", 21) + "B." + canonicalSecret},
		{name: "secret alias", value: "mcp1." + testWorkspaceID + "." + canonicalLocator + "." + strings.Repeat("A", 42) + "B"},
		{name: "locator carriage return", value: "mcp1." + testWorkspaceID + "." + canonicalLocator[:11] + "\r" + canonicalLocator[11:] + "." + canonicalSecret},
		{name: "locator line feed", value: "mcp1." + testWorkspaceID + "." + canonicalLocator[:11] + "\n" + canonicalLocator[11:] + "." + canonicalSecret},
		{name: "secret carriage return", value: "mcp1." + testWorkspaceID + "." + canonicalLocator + "." + canonicalSecret[:21] + "\r" + canonicalSecret[21:]},
		{name: "secret line feed", value: "mcp1." + testWorkspaceID + "." + canonicalLocator + "." + canonicalSecret[:21] + "\n" + canonicalSecret[21:]},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			if _, err := ParseCredential(item.value); err == nil {
				t.Fatal("ParseCredential() error = nil")
			}
		})
	}
}

func TestClaimSecretUses256BitsAndStableHash(t *testing.T) {
	secret, hash, err := NewClaimSecret(bytes.NewReader(bytes.Repeat([]byte{0x73}, 32)))
	if err != nil {
		t.Fatalf("NewClaimSecret() error = %v", err)
	}
	if len(secret) != 43 || len(hash) != 32 {
		t.Fatalf("secret/hash lengths = %d/%d", len(secret), len(hash))
	}
	parsed, err := HashClaimSecret(secret)
	if err != nil || subtle.ConstantTimeCompare(parsed, hash) != 1 {
		t.Fatalf("HashClaimSecret() mismatch or error = %v", err)
	}
}

func TestClaimSecretRejectsNoncanonicalValues(t *testing.T) {
	tests := map[string]string{
		"padding":         "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"alias":           strings.Repeat("A", 42) + "B",
		"carriage return": strings.Repeat("A", 21) + "\r" + strings.Repeat("A", 22),
		"line feed":       strings.Repeat("A", 21) + "\n" + strings.Repeat("A", 22),
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := HashClaimSecret(value); err == nil {
				t.Fatal("HashClaimSecret() error = nil")
			}
		})
}
}
```

- [ ] **Step 7: Run the bearer tests red**

```bash
go test ./internal/secure -run 'TestCredential|TestClaim'
```

Expected: FAIL because credential functions are undefined.

- [ ] **Step 8: Write exact bearer and claim-secret generation**

Create `internal/secure/credential.go` with:

```go
package secure

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

const credentialDomain = "mcpaste-credential-v1"
const claimDomain = "mcpaste-pairing-claim-v1"

type IssuedCredential struct {
	Kind      string
	Token     string
	Locator   string
	Hash      []byte
}

type ParsedCredential struct {
	WorkspaceID string
	Locator     string
	Hash        []byte
}

func NewCredential(workspaceID, kind string, random Random) (IssuedCredential, error) {
	if !validUUID(workspaceID) || (kind != "full" && kind != "connector") {
		return IssuedCredential{}, errors.New("credential metadata is invalid")
	}
	locatorBytes, err := randomBytes(random, 16)
	if err != nil {
		return IssuedCredential{}, err
	}
	secret, err := randomBytes(random, 32)
	if err != nil {
		return IssuedCredential{}, err
	}
	locator := base64.RawURLEncoding.EncodeToString(locatorBytes)
	token := "mcp1." + workspaceID + "." + locator + "." + base64.RawURLEncoding.EncodeToString(secret)
	return IssuedCredential{Kind: kind, Token: token, Locator: locator, Hash: hashSecret(credentialDomain, secret)}, nil
}

func ParseCredential(token string) (ParsedCredential, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "mcp1" || !validUUID(parts[1]) {
		return ParsedCredential{}, errors.New("credential is invalid")
	}
	if _, err := decodeCanonicalRawURL(parts[2], 16); err != nil {
		return ParsedCredential{}, errors.New("credential is invalid")
	}
	secret, err := decodeCanonicalRawURL(parts[3], 32)
	if err != nil {
		return ParsedCredential{}, errors.New("credential is invalid")
	}
	return ParsedCredential{WorkspaceID: parts[1], Locator: parts[2], Hash: hashSecret(credentialDomain, secret)}, nil
}

func NewClaimSecret(random Random) (string, []byte, error) {
	secret, err := randomBytes(random, 32)
	if err != nil {
		return "", nil, err
	}
	return base64.RawURLEncoding.EncodeToString(secret), hashSecret(claimDomain, secret), nil
}

func HashClaimSecret(value string) ([]byte, error) {
	secret, err := decodeCanonicalRawURL(value, 32)
	if err != nil {
		return nil, errors.New("claim secret is invalid")
	}
	return hashSecret(claimDomain, secret), nil
}

func hashSecret(domain string, secret []byte) []byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte(domain))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(secret)
	return digest.Sum(nil)
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(compact)
	return err == nil && len(decoded) == 16 && strings.ToLower(value) == value
}
```

- [ ] **Step 9: Make bearer and claim tests green**

```bash
gofmt -w internal/secure/credential.go internal/secure/credential_test.go
go test -race ./internal/secure -run 'TestCredential|TestClaim'
```

Expected: PASS. Bearer locator and secret segments plus standalone claim secrets reject padding, non-zero trailing bits, CR, and LF through the same expected-length/strict/round-trip decoder. Inspect test failure output first if it does not; never print `issued.Token` or `secret` while diagnosing.

- [ ] **Step 10: Write recovery tests before recovery code**

Create `internal/secure/argon2_test.go` with:

```go
package secure

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

func TestArgon2LimiterBoundsConcurrencyCancellationAndRelease(t *testing.T) {
	limiter := processArgon2Limiter
	if capacity := cap(limiter.slots); capacity != 2 {
		t.Fatalf("limiter capacity = %d", capacity)
	}
	if occupied := len(limiter.slots); occupied != 0 {
		t.Fatalf("process limiter initially occupied = %d", occupied)
	}
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	results := make(chan error, 2)
	var active atomic.Int32
	var maximum atomic.Int32
	derive := func(_ []byte, _ []byte, _ uint32, _ uint32, _ uint8, length uint32) []byte {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return make([]byte, length)
	}
	for index := 0; index < 2; index++ {
		go func() {
			_, err := limiter.key(context.Background(), nil, nil, 1, 1, 1, 32, derive)
			results <- err
		}()
	}
	<-started
	<-started
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := limiter.key(canceled, nil, nil, 1, 1, 1, 32, derive); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled waiter did not return context cancellation")
	}
	if len(started) != 0 {
		t.Fatal("canceled waiter entered Argon2 derivation")
	}
	close(release)
	for index := 0; index < 2; index++ {
		if err := <-results; err != nil {
			t.Fatal("admitted derivation returned an error")
		}
	}
	if maximum.Load() != 2 || active.Load() != 0 || len(limiter.slots) != 0 {
		t.Fatalf("maximum/active/slots = %d/%d/%d", maximum.Load(), active.Load(), len(limiter.slots))
	}
	called := false
	if _, err := limiter.key(context.Background(), nil, nil, 1, 1, 1, 32, func(_ []byte, _ []byte, _ uint32, _ uint32, _ uint8, length uint32) []byte {
		called = true
		return make([]byte, length)
	}); err != nil || !called {
		t.Fatal("released limiter did not admit a subsequent derivation")
	}
}

func TestRecoveryPermitRejectsNilAndZeroHandles(t *testing.T) {
	code := "mcr1." + testWorkspaceID + "." + strings.Repeat("A", 22) + "." + strings.Repeat("A", 43)
	verifier := RecoveryVerifier{
		Salt:      make([]byte, 16),
		Hash:      make([]byte, 32),
		Version:   argon2.Version,
		Time:      recoveryTime,
		MemoryKiB: recoveryMemoryKiB,
		Threads:   recoveryThreads,
	}
	tests := []struct {
		name   string
		permit *RecoveryPermit
	}{
		{name: "nil handle", permit: nil},
		{name: "zero handle", permit: &RecoveryPermit{}},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			if err := item.permit.Release(); !errors.Is(err, errInvalidRecoveryPermit) {
				t.Fatal("Release did not reject invalid recovery permit")
			}
			if _, err := NewRecoveryWithPermit(context.Background(), item.permit, testWorkspaceID, nil); !errors.Is(err, errInvalidRecoveryPermit) {
				t.Fatal("NewRecoveryWithPermit did not reject invalid recovery permit")
			}
			if err := VerifyRecoveryWithPermit(context.Background(), item.permit, code, testWorkspaceID, strings.Repeat("A", 22), verifier); !errors.Is(err, errInvalidRecoveryPermit) {
				t.Fatal("VerifyRecoveryWithPermit did not reject invalid recovery permit")
			}
		})
	}
}

func TestRecoveryPermitCopiesShareStateAndReleaseOnce(t *testing.T) {
	limiter := newArgon2Limiter(1)
	permit, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal("acquire recovery permit failed")
	}
	copied := *permit
	if copied.state == nil || copied.state != permit.state {
		t.Fatal("copied handle did not share permit state")
	}
	if err := copied.Release(); err != nil {
		t.Fatal("copied handle release failed")
	}
	if occupied := len(limiter.slots); occupied != 0 {
		t.Fatalf("slots after copied handle release = %d", occupied)
	}
	if err := permit.Release(); err != nil {
		t.Fatal("repeated release through original handle failed")
	}
	if occupied := len(limiter.slots); occupied != 0 {
		t.Fatalf("slots after original handle release = %d", occupied)
	}
	if _, err := permit.key(context.Background(), nil, nil, 1, 1, 1, 32, func(_ []byte, _ []byte, _ uint32, _ uint32, _ uint8, length uint32) []byte {
		return make([]byte, length)
	}); !errors.Is(err, errInvalidRecoveryPermit) {
		t.Fatal("released shared permit state accepted derivation")
	}
	next, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal("slot was not reusable after shared release")
	}
	if err := next.Release(); err != nil {
		t.Fatal("release next recovery permit failed")
	}
}

func TestRecoveryPermitSerializesDerivations(t *testing.T) {
	limiter := newArgon2Limiter(1)
	permit, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal("acquire recovery permit failed")
	}
	defer permit.Release()

	var active atomic.Int32
	var maximum atomic.Int32
	derive := func(started chan<- struct{}, finish <-chan struct{}) argon2KeyFunc {
		return func(_ []byte, _ []byte, _ uint32, _ uint32, _ uint8, length uint32) []byte {
			current := active.Add(1)
			for {
				observed := maximum.Load()
				if current <= observed || maximum.CompareAndSwap(observed, current) {
					break
				}
			}
			started <- struct{}{}
			<-finish
			active.Add(-1)
			return make([]byte, length)
		}
	}

	firstStarted := make(chan struct{}, 1)
	firstFinish := make(chan struct{})
	firstResult := make(chan error, 1)
	go func() {
		_, keyErr := permit.key(context.Background(), nil, nil, 1, 1, 1, 32, derive(firstStarted, firstFinish))
		firstResult <- keyErr
	}()
	<-firstStarted
	if permit.state.mu.TryLock() {
		permit.state.mu.Unlock()
		t.Fatal("permit mutex was not held across active derivation")
	}

	secondStarted := make(chan struct{}, 1)
	secondFinish := make(chan struct{})
	secondResult := make(chan error, 1)
	secondCalled := make(chan struct{})
	go func() {
		close(secondCalled)
		_, keyErr := permit.key(context.Background(), nil, nil, 1, 1, 1, 32, derive(secondStarted, secondFinish))
		secondResult <- keyErr
	}()
	<-secondCalled
	select {
	case <-secondStarted:
		t.Fatal("second derivation entered while first derivation was active")
	default:
	}
	close(firstFinish)
	if err := <-firstResult; err != nil {
		t.Fatal("first derivation failed")
	}
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("sequential second derivation did not start")
	}
	close(secondFinish)
	if err := <-secondResult; err != nil {
		t.Fatal("second derivation failed")
	}
	if maximum.Load() != 1 || active.Load() != 0 {
		t.Fatalf("maximum/active derivations = %d/%d", maximum.Load(), active.Load())
	}
}

func TestRecoveryPermitReleaseWaitsForActiveDerivation(t *testing.T) {
	limiter := newArgon2Limiter(1)
	permit, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal("acquire recovery permit failed")
	}
	started := make(chan struct{})
	finish := make(chan struct{})
	deriveDone := make(chan error, 1)
	go func() {
		_, keyErr := permit.key(context.Background(), nil, nil, 1, 1, 1, 32, func(_ []byte, _ []byte, _ uint32, _ uint32, _ uint8, length uint32) []byte {
			close(started)
			<-finish
			return make([]byte, length)
		})
		deriveDone <- keyErr
	}()
	<-started
	releaseEntered := make(chan struct{})
	releaseDone := make(chan error, 1)
	go func() {
		close(releaseEntered)
		releaseDone <- permit.Release()
	}()
	<-releaseEntered
	if permit.state.mu.TryLock() {
		permit.state.mu.Unlock()
		t.Fatal("permit mutex was not held during derivation")
	}
	if occupied := len(limiter.slots); occupied != 1 {
		t.Fatalf("slot released during active derivation = %d", occupied)
	}
	select {
	case err := <-releaseDone:
		if err != nil {
			t.Fatal("Release failed during active derivation")
		}
		t.Fatal("Release returned during active derivation")
	default:
	}
	close(finish)
	if err := <-deriveDone; err != nil {
		t.Fatal("active derivation failed")
	}
	select {
	case err := <-releaseDone:
		if err != nil {
			t.Fatal("Release failed after active derivation")
		}
	case <-time.After(time.Second):
		t.Fatal("Release did not return after derivation")
	}
	if occupied := len(limiter.slots); occupied != 0 {
		t.Fatalf("slot remained occupied after Release = %d", occupied)
	}
}

func TestProductionArgon2CapacityAndSingleCallSite(t *testing.T) {
	if processArgon2Capacity != 2 || cap(processArgon2Limiter.slots) != processArgon2Capacity {
		t.Fatalf("process capacity metadata = %d/%d", processArgon2Capacity, cap(processArgon2Limiter.slots))
	}
	if argon2.Version != 0x13 {
		t.Fatalf("Argon2 version = %d", argon2.Version)
	}
}
```

Create `internal/secure/recovery_test.go` with:

```go
package secure

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRecoveryRoundTripWrongCodeAndCorruption(t *testing.T) {
	randomInput := make([]byte, 64)
	copy(randomInput[:16], bytes.Repeat([]byte{0x21}, 16))
	copy(randomInput[16:48], bytes.Repeat([]byte{0x32}, 32))
	copy(randomInput[48:], bytes.Repeat([]byte{0x43}, 16))
	issued, err := NewRecovery(context.Background(), testWorkspaceID, bytes.NewReader(randomInput))
	if err != nil {
		t.Fatalf("NewRecovery() error = %v", err)
	}
	workspaceID, locator, err := RecoveryLocator(issued.Code)
	if err != nil || workspaceID != testWorkspaceID || locator != issued.Locator {
		t.Fatalf("RecoveryLocator() = %q, %q, %v", workspaceID, locator, err)
	}
	if err := VerifyRecovery(context.Background(), issued.Code, testWorkspaceID, issued.Locator, issued.Verifier); err != nil {
		t.Fatalf("VerifyRecovery() error = %v", err)
	}
	wrong := issued.Code[:len(issued.Code)-1] + "A"
	if err := VerifyRecovery(context.Background(), wrong, testWorkspaceID, issued.Locator, issued.Verifier); err == nil {
		t.Fatal("wrong recovery code verified")
	}
	corrupt := issued.Verifier
	corrupt.Hash = []byte{0x01}
	if err := VerifyRecovery(context.Background(), issued.Code, testWorkspaceID, issued.Locator, corrupt); err == nil {
		t.Fatal("corrupt verifier accepted")
	}
}

func TestRecoveryRejectsWrongLocatorAndParameters(t *testing.T) {
	randomInput := make([]byte, 64)
	copy(randomInput[:16], bytes.Repeat([]byte{0x51}, 16))
	copy(randomInput[16:48], bytes.Repeat([]byte{0x62}, 32))
	copy(randomInput[48:], bytes.Repeat([]byte{0x73}, 16))
	issued, err := NewRecovery(context.Background(), testWorkspaceID, bytes.NewReader(randomInput))
	if err != nil {
		t.Fatalf("NewRecovery() error = %v", err)
	}
	if err := VerifyRecovery(context.Background(), issued.Code, testWorkspaceID, "AAAAAAAAAAAAAAAAAAAAAA", issued.Verifier); err == nil {
		t.Fatal("wrong locator verified")
	}
	changed := issued.Verifier
	changed.MemoryKiB = 32768
	if err := VerifyRecovery(context.Background(), issued.Code, testWorkspaceID, issued.Locator, changed); err == nil {
		t.Fatal("unsupported Argon2 parameters accepted")
	}
}

func TestRecoveryRejectsMalformedCodesGenerically(t *testing.T) {
	validLocator := strings.Repeat("A", 22)
	validSecret := strings.Repeat("A", 43)
	code := func(workspaceID, locator, secret string) string {
		return strings.Join([]string{"mcr1", workspaceID, locator, secret}, ".")
	}
	tests := []struct {
		name  string
		value string
	}{
		{name: "bad prefix", value: "mcr2." + testWorkspaceID + "." + validLocator + "." + validSecret},
		{name: "noncanonical workspace UUID", value: code("AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", validLocator, validSecret)},
		{name: "bad workspace UUID", value: code("not-a-workspace-uuid", validLocator, validSecret)},
		{name: "wrong segment count", value: strings.Join([]string{"mcr1", testWorkspaceID, validLocator}, ".")},
		{name: "locator wrong length", value: code(testWorkspaceID, strings.Repeat("A", 21), validSecret)},
		{name: "locator padding", value: code(testWorkspaceID, validLocator+"=", validSecret)},
		{name: "secret wrong length", value: code(testWorkspaceID, validLocator, strings.Repeat("A", 42))},
		{name: "secret padding", value: code(testWorkspaceID, validLocator, validSecret+"=")},
		{name: "invalid locator base64", value: code(testWorkspaceID, strings.Repeat("!", 22), validSecret)},
		{name: "invalid secret base64", value: code(testWorkspaceID, validLocator, strings.Repeat("!", 43))},
		{name: "noncanonical locator alias", value: code(testWorkspaceID, strings.Repeat("A", 21)+"B", validSecret)},
		{name: "noncanonical secret alias", value: code(testWorkspaceID, validLocator, strings.Repeat("A", 42)+"B")},
		{name: "locator carriage return", value: code(testWorkspaceID, validLocator[:11]+"\r"+validLocator[11:], validSecret)},
		{name: "locator line feed", value: code(testWorkspaceID, validLocator[:11]+"\n"+validLocator[11:], validSecret)},
		{name: "secret carriage return", value: code(testWorkspaceID, validLocator, validSecret[:21]+"\r"+validSecret[21:])},
		{name: "secret line feed", value: code(testWorkspaceID, validLocator, validSecret[:21]+"\n"+validSecret[21:])},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			_, _, locatorErr := RecoveryLocator(item.value)
			if !errors.Is(locatorErr, ErrInvalidRecovery) {
				t.Fatal("RecoveryLocator did not return generic invalid-recovery error")
			}
			verifyErr := VerifyRecovery(context.Background(), item.value, testWorkspaceID, validLocator, RecoveryVerifier{})
			if !errors.Is(verifyErr, ErrInvalidRecovery) {
				t.Fatal("VerifyRecovery did not return generic invalid-recovery error")
			}
		})
	}
}

func TestNewRecoveryHonorsCanceledContextBeforeArgon2(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	randomInput := bytes.NewReader(bytes.Repeat([]byte{0x91}, 64))
	if _, err := NewRecovery(ctx, testWorkspaceID, randomInput); !errors.Is(err, context.Canceled) {
		t.Fatal("NewRecovery() did not return context cancellation")
	}
}

func TestRecoveryPermitSupportsSequentialGenerationAndVerification(t *testing.T) {
	permit, err := AcquireRecoveryPermit(context.Background())
	if err != nil {
		t.Fatal("acquire recovery permit failed")
	}
	defer permit.Release()
	randomInput := make([]byte, 64)
	copy(randomInput[:16], bytes.Repeat([]byte{0xa1}, 16))
	copy(randomInput[16:48], bytes.Repeat([]byte{0xa2}, 32))
	copy(randomInput[48:], bytes.Repeat([]byte{0xa3}, 16))
	issued, err := NewRecoveryWithPermit(context.Background(), permit, testWorkspaceID, bytes.NewReader(randomInput))
	if err != nil {
		t.Fatal("permit-backed recovery generation failed")
	}
	if occupied := len(processArgon2Limiter.slots); occupied != 1 {
		t.Fatalf("slots after generation = %d", occupied)
	}
	if err := VerifyRecoveryWithPermit(context.Background(), permit, issued.Code, testWorkspaceID, issued.Locator, issued.Verifier); err != nil {
		t.Fatal("permit-backed recovery verification failed")
	}
	if occupied := len(processArgon2Limiter.slots); occupied != 1 {
		t.Fatalf("slots after verification = %d", occupied)
	}
}
```

- [ ] **Step 11: Run recovery tests red**

```bash
go test ./internal/secure -run TestRecovery
```

Expected: FAIL because recovery types and functions are undefined. The malformed-code table covers every format boundary, including separate locator/secret CR and LF cases, without interpolating the code, locator, secret, parser error, or verifier error into failure output; only the non-sensitive subtest name and fixed generic assertion text may appear.

- [ ] **Step 12: Write the Argon2id recovery format**

Create `internal/secure/argon2.go` with:

```go
package secure

import (
	"context"
	"errors"
	"sync"

	"golang.org/x/crypto/argon2"
)

const processArgon2Capacity = 2

type argon2KeyFunc func([]byte, []byte, uint32, uint32, uint8, uint32) []byte

type argon2Limiter struct {
	slots chan struct{}
}

type RecoveryPermit struct {
	state *recoveryPermitState
}

type recoveryPermitState struct {
	limiter  *argon2Limiter
	mu       sync.Mutex
	released bool
}

var processArgon2Limiter = newArgon2Limiter(processArgon2Capacity)

var errInvalidRecoveryPermit = errors.New("recovery permit is invalid")

func newArgon2Limiter(capacity int) *argon2Limiter {
	return &argon2Limiter{slots: make(chan struct{}, capacity)}
}

func (l *argon2Limiter) acquire(ctx context.Context) (*RecoveryPermit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case l.slots <- struct{}{}:
		permit := &RecoveryPermit{state: &recoveryPermitState{limiter: l}}
		if err := ctx.Err(); err != nil {
			_ = permit.Release()
			return nil, err
		}
		return permit, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func AcquireRecoveryPermit(ctx context.Context) (*RecoveryPermit, error) {
	return processArgon2Limiter.acquire(ctx)
}

func (p *RecoveryPermit) Release() error {
	if p == nil || p.state == nil {
		return errInvalidRecoveryPermit
	}
	state := p.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.limiter == nil {
		return errInvalidRecoveryPermit
	}
	if state.released {
		return nil
	}
	state.released = true
	<-state.limiter.slots
	return nil
}

func (p *RecoveryPermit) key(
	ctx context.Context,
	password []byte,
	salt []byte,
	timeCost uint32,
	memoryKiB uint32,
	threads uint8,
	length uint32,
	derive argon2KeyFunc,
) ([]byte, error) {
	if p == nil || p.state == nil || derive == nil {
		return nil, errInvalidRecoveryPermit
	}
	state := p.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.limiter == nil || state.released {
		return nil, errInvalidRecoveryPermit
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return derive(password, salt, timeCost, memoryKiB, threads, length), nil
}

func (l *argon2Limiter) key(
	ctx context.Context,
	password []byte,
	salt []byte,
	timeCost uint32,
	memoryKiB uint32,
	threads uint8,
	length uint32,
	derive argon2KeyFunc,
) ([]byte, error) {
	permit, err := l.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer permit.Release()
	if permit.state == nil || permit.state.limiter != l {
		return nil, errInvalidRecoveryPermit
	}
	return permit.key(ctx, password, salt, timeCost, memoryKiB, threads, length, derive)
}

func recoveryKeyWithPermit(
	ctx context.Context,
	permit *RecoveryPermit,
	secret []byte,
	salt []byte,
	timeCost uint32,
	memoryKiB uint32,
	threads uint8,
	length uint32,
) ([]byte, error) {
	if permit == nil || permit.state == nil || permit.state.limiter != processArgon2Limiter {
		return nil, errInvalidRecoveryPermit
	}
	return permit.key(ctx, secret, salt, timeCost, memoryKiB, threads, length, argon2.IDKey)
}

```

Create `internal/secure/recovery.go` with:

```go
package secure

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"

	"golang.org/x/crypto/argon2"
)

const recoveryTime uint32 = 3
const recoveryMemoryKiB uint32 = 64 * 1024
const recoveryThreads uint8 = 4
const recoveryHashLength uint32 = 32

var ErrInvalidRecovery = errors.New("recovery code is invalid")

type RecoveryVerifier struct {
	Salt       []byte
	Hash       []byte
	Version    int
	Time       uint32
	MemoryKiB  uint32
	Threads    uint8
}

type IssuedRecovery struct {
	Code     string
	Locator  string
	Verifier RecoveryVerifier
}

func NewRecovery(ctx context.Context, workspaceID string, random Random) (IssuedRecovery, error) {
	permit, err := AcquireRecoveryPermit(ctx)
	if err != nil {
		return IssuedRecovery{}, err
	}
	defer permit.Release()
	return NewRecoveryWithPermit(ctx, permit, workspaceID, random)
}

func NewRecoveryWithPermit(ctx context.Context, permit *RecoveryPermit, workspaceID string, random Random) (IssuedRecovery, error) {
	if permit == nil || permit.state == nil || permit.state.limiter != processArgon2Limiter {
		return IssuedRecovery{}, errInvalidRecoveryPermit
	}
	if !validUUID(workspaceID) {
		return IssuedRecovery{}, ErrInvalidRecovery
	}
	locatorBytes, err := randomBytes(random, 16)
	if err != nil {
		return IssuedRecovery{}, err
	}
	secret, err := randomBytes(random, 32)
	if err != nil {
		return IssuedRecovery{}, err
	}
	salt, err := randomBytes(random, 16)
	if err != nil {
		return IssuedRecovery{}, err
	}
	locator := base64.RawURLEncoding.EncodeToString(locatorBytes)
	code := "mcr1." + workspaceID + "." + locator + "." + base64.RawURLEncoding.EncodeToString(secret)
	hash, err := recoveryKeyWithPermit(ctx, permit, secret, salt, recoveryTime, recoveryMemoryKiB, recoveryThreads, recoveryHashLength)
	if err != nil {
		return IssuedRecovery{}, err
	}
	verifier := RecoveryVerifier{
		Salt:      salt,
		Hash:      hash,
		Version:   argon2.Version,
		Time:      recoveryTime,
		MemoryKiB: recoveryMemoryKiB,
		Threads:   recoveryThreads,
	}
	return IssuedRecovery{Code: code, Locator: locator, Verifier: verifier}, nil
}

func RecoveryLocator(code string) (string, string, error) {
	parts := strings.Split(code, ".")
	if len(parts) != 4 || parts[0] != "mcr1" || !validUUID(parts[1]) {
		return "", "", ErrInvalidRecovery
	}
	if _, err := decodeCanonicalRawURL(parts[2], 16); err != nil {
		return "", "", ErrInvalidRecovery
	}
	if _, err := decodeCanonicalRawURL(parts[3], 32); err != nil {
		return "", "", ErrInvalidRecovery
	}
	return parts[1], parts[2], nil
}

func VerifyRecovery(ctx context.Context, code, workspaceID, locator string, verifier RecoveryVerifier) error {
	permit, err := AcquireRecoveryPermit(ctx)
	if err != nil {
		return err
	}
	defer permit.Release()
	return VerifyRecoveryWithPermit(ctx, permit, code, workspaceID, locator, verifier)
}

func VerifyRecoveryWithPermit(ctx context.Context, permit *RecoveryPermit, code, workspaceID, locator string, verifier RecoveryVerifier) error {
	if permit == nil || permit.state == nil || permit.state.limiter != processArgon2Limiter {
		return errInvalidRecoveryPermit
	}
	parsedWorkspace, parsedLocator, err := RecoveryLocator(code)
	if err != nil || parsedWorkspace != workspaceID || parsedLocator != locator {
		return ErrInvalidRecovery
	}
	if verifier.Version != argon2.Version || verifier.Time != recoveryTime || verifier.MemoryKiB != recoveryMemoryKiB || verifier.Threads != recoveryThreads || len(verifier.Salt) != 16 || len(verifier.Hash) != 32 {
		return ErrInvalidRecovery
	}
	secretText := strings.Split(code, ".")[3]
	secret, err := decodeCanonicalRawURL(secretText, 32)
	if err != nil {
		return ErrInvalidRecovery
	}
	actual, err := recoveryKeyWithPermit(ctx, permit, secret, verifier.Salt, verifier.Time, verifier.MemoryKiB, verifier.Threads, uint32(len(verifier.Hash)))
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(actual, verifier.Hash) != 1 {
		return ErrInvalidRecovery
	}
	return nil
}
```

- [ ] **Step 13: Run all security tests and dependency cleanup**

```bash
gofmt -w internal/secure/argon2.go internal/secure/argon2_test.go internal/secure/recovery.go internal/secure/recovery_test.go
go test -race ./internal/secure -run 'TestArgon2LimiterBoundsConcurrencyCancellationAndRelease|TestRecoveryPermitRejectsNilAndZeroHandles|TestRecoveryPermitCopiesShareStateAndReleaseOnce|TestRecoveryPermitSerializesDerivations|TestRecoveryPermitReleaseWaitsForActiveDerivation|TestProductionArgon2CapacityAndSingleCallSite|TestRecoveryRoundTripWrongCodeAndCorruption|TestRecoveryRejectsWrongLocatorAndParameters|TestRecoveryRejectsMalformedCodesGenerically|TestNewRecoveryHonorsCanceledContextBeforeArgon2|TestRecoveryPermitSupportsSequentialGenerationAndVerification' -count=1
go test -race ./internal/secure
test "$(rg -n 'argon2\.IDKey' internal/secure --glob '*.go' --glob '!*_test.go' | wc -l | tr -d ' ')" = "1"
go mod tidy
go mod verify
git diff --check -- internal/secure go.mod go.sum
```

Expected: all focused limiter/recovery tests and the complete security suite pass. `RecoveryPermit` is an exported opaque concrete handle whose only field points to unexported shared state; every acquire and `WithPermit` API uses exact `*RecoveryPermit`, so an external wrapper or embedded type is not accepted. The tests prove nil and zero handles return `errInvalidRecoveryPermit` without panic, copied handles share one state and cannot double-release, one state's mutex spans each complete derivation, two calls on one handle serialize, `Release` waits for active work, sequential generation then verification can reuse the held slot, process maximum concurrency remains 2, a canceled third waiter never enters derivation, and permits return to zero. The production-source scan finds exactly the guarded `argon2.IDKey` call in `recoveryKeyWithPermit`; `NewRecovery` and `VerifyRecovery` acquire and release their own concrete handles, while their `WithPermit` variants reuse the same shared state without reacquiring. Every malformed parser and verification result, including CR, LF, padding, and non-zero trailing-bit aliases, is `ErrInvalidRecovery`, and failure output contains no malformed code component. x/crypto remains a direct dependency, pgx remains direct because migration code imports it, and module verification passes.

- [ ] **Step 14: Commit the security primitives**

```bash
git add internal/secure go.mod go.sum
git commit -m "feat: add identity security primitives"
```

Expected: one commit with AES envelope, bearer, claim, and Argon2id recovery code plus tests. No rendered runtime secret appears in the diff.

## Task 5: Load stateful server configuration and PostgreSQL readiness

**Files:**

- Modify: `internal/config/config_test.go`
- Modify: `internal/config/config.go`
- Create: `internal/database/pool_test.go`
- Create: `internal/database/pool.go`

- [ ] **Step 1: Replace configuration tests with the complete Phase 2 contract**

Replace `internal/config/config_test.go` with:

```go
package config

import (
	"encoding/base64"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsWithRequiredSecrets(t *testing.T) {
	cfg, err := Load(mapLookup(requiredValues()))
	if err != nil {
		t.Fatal("Load() returned an error for valid defaults")
	}
	if cfg.Environment != Development {
		t.Fatalf("environment = %q", cfg.Environment)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTP address = %q", cfg.HTTPAddr)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("log level = %v", cfg.LogLevel)
	}
	if cfg.DatabaseURL == "" {
		t.Fatal("database URL is empty")
	}
	if cfg.ActiveKeyID != "test-key" {
		t.Fatalf("active key ID = %q", cfg.ActiveKeyID)
	}
	if cfg.EncryptionKeys == "" {
		t.Fatal("encryption keyring is empty")
	}
	if cfg.CleanupInterval != 15*time.Minute || len(cfg.TrustedProxyCIDRs) != 0 {
		t.Fatalf("cleanup/proxies = %v/%d", cfg.CleanupInterval, len(cfg.TrustedProxyCIDRs))
	}
}

func TestLoadOverrides(t *testing.T) {
	values := requiredValues()
	values["MCPASTE_ENV"] = "production"
	values["MCPASTE_HTTP_ADDR"] = "127.0.0.1:9090"
	values["MCPASTE_LOG_LEVEL"] = "debug"
	values["MCPASTE_CLEANUP_INTERVAL"] = "10m"
	values["MCPASTE_TRUSTED_PROXY_CIDRS"] = "127.0.0.1/32,10.0.0.0/8"
	cfg, err := Load(mapLookup(values))
	if err != nil {
		t.Fatal("Load() returned an error for valid overrides")
	}
	if cfg.Environment != Production {
		t.Fatalf("environment = %q", cfg.Environment)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" {
		t.Fatalf("HTTP address = %q", cfg.HTTPAddr)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("log level = %v", cfg.LogLevel)
	}
	if cfg.CleanupInterval != 10*time.Minute || len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatalf("cleanup/proxies = %v/%d", cfg.CleanupInterval, len(cfg.TrustedProxyCIDRs))
	}
}

func TestLoadRejectsInvalidValuesWithoutEchoingSecrets(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "environment", key: "MCPASTE_ENV", value: "staging"},
		{name: "address", key: "MCPASTE_HTTP_ADDR", value: "8080"},
		{name: "log level", key: "MCPASTE_LOG_LEVEL", value: "verbose"},
		{name: "database", key: "MCPASTE_DATABASE_URL", value: "database-secret-marker"},
		{name: "active key", key: "MCPASTE_ACTIVE_KEY_ID", value: "bad key"},
		{name: "keyring", key: "MCPASTE_ENCRYPTION_KEYS", value: "keyring-secret-marker"},
		{name: "cleanup", key: "MCPASTE_CLEANUP_INTERVAL", value: "2s"},
		{name: "proxy", key: "MCPASTE_TRUSTED_PROXY_CIDRS", value: "proxy-secret-marker"},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			values := requiredValues()
			values[item.key] = item.value
			_, err := Load(mapLookup(values))
			if err == nil {
				t.Fatal("Load() error = nil")
			}
			if strings.Contains(err.Error(), item.value) {
				t.Fatal("configuration error echoed the rejected value")
			}
		})
	}
}

func TestLoadRequiresTrustedProxyInProduction(t *testing.T) {
	values := requiredValues()
	values["MCPASTE_ENV"] = "production"
	if _, err := Load(mapLookup(values)); err == nil {
		t.Fatal("production without trusted proxy accepted")
	}
}

func requiredValues() map[string]string {
	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	return map[string]string{
		"MCPASTE_DATABASE_URL":      "postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable",
		"MCPASTE_ACTIVE_KEY_ID":     "test-key",
		"MCPASTE_ENCRYPTION_KEYS":   "test-key:" + key,
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
```

- [ ] **Step 2: Run configuration tests red**

```bash
go test ./internal/config
```

Expected: FAIL because `Config` lacks Phase 2 fields and current `Load` does not require stateful values.

- [ ] **Step 3: Replace configuration parsing with the complete version**

Replace `internal/config/config.go` with:

```go
package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/1yoouoo/mcpaste/internal/secure"
)

type Environment string

const (
	Development Environment = "development"
	Test        Environment = "test"
	Production  Environment = "production"
)

type Config struct {
	Environment       Environment
	HTTPAddr          string
	LogLevel          slog.Level
	DatabaseURL       string
	ActiveKeyID       string
	EncryptionKeys    string
	CleanupInterval   time.Duration
	TrustedProxyCIDRs []*net.IPNet
}

type LookupEnv func(string) (string, bool)

func LoadOS() (Config, error) {
	return Load(os.LookupEnv)
}

func Load(lookup LookupEnv) (Config, error) {
	cfg := Config{
		Environment:     Development,
		HTTPAddr:        ":8080",
		LogLevel:        slog.LevelInfo,
		CleanupInterval: 15 * time.Minute,
	}
	if value, ok := nonEmpty(lookup, "MCPASTE_ENV"); ok {
		switch Environment(value) {
		case Development, Test, Production:
			cfg.Environment = Environment(value)
		default:
			return Config{}, fmt.Errorf("MCPASTE_ENV must be development, test, or production")
		}
	}
	if value, ok := nonEmpty(lookup, "MCPASTE_HTTP_ADDR"); ok {
		cfg.HTTPAddr = value
	}
	if err := validateHTTPAddr(cfg.HTTPAddr); err != nil {
		return Config{}, fmt.Errorf("MCPASTE_HTTP_ADDR: %w", err)
	}
	if value, ok := nonEmpty(lookup, "MCPASTE_LOG_LEVEL"); ok {
		level, err := parseLogLevel(value)
		if err != nil {
			return Config{}, fmt.Errorf("MCPASTE_LOG_LEVEL: %w", err)
		}
		cfg.LogLevel = level
	}
	databaseURL, ok := nonEmpty(lookup, "MCPASTE_DATABASE_URL")
	if !ok || !validDatabaseURL(databaseURL) {
		return Config{}, fmt.Errorf("MCPASTE_DATABASE_URL must be a PostgreSQL URL")
	}
	cfg.DatabaseURL = databaseURL
	activeKeyID, ok := nonEmpty(lookup, "MCPASTE_ACTIVE_KEY_ID")
	if !ok {
		return Config{}, fmt.Errorf("MCPASTE_ACTIVE_KEY_ID is required")
	}
	keyring, ok := nonEmpty(lookup, "MCPASTE_ENCRYPTION_KEYS")
	if !ok {
		return Config{}, fmt.Errorf("MCPASTE_ENCRYPTION_KEYS is required")
	}
	if _, err := secure.ParseKeyring(activeKeyID, keyring, secure.SystemRandom{}); err != nil {
		return Config{}, fmt.Errorf("MCPASTE_ENCRYPTION_KEYS or MCPASTE_ACTIVE_KEY_ID is invalid")
	}
	cfg.ActiveKeyID = activeKeyID
	cfg.EncryptionKeys = keyring
	if value, ok := nonEmpty(lookup, "MCPASTE_CLEANUP_INTERVAL"); ok {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < time.Minute || parsed > time.Hour {
			return Config{}, fmt.Errorf("MCPASTE_CLEANUP_INTERVAL must be from 1m through 1h")
		}
		cfg.CleanupInterval = parsed
	}
	if value, ok := nonEmpty(lookup, "MCPASTE_TRUSTED_PROXY_CIDRS"); ok {
		for _, item := range strings.Split(value, ",") {
			_, network, err := net.ParseCIDR(strings.TrimSpace(item))
			if err != nil {
				return Config{}, fmt.Errorf("MCPASTE_TRUSTED_PROXY_CIDRS contains an invalid CIDR")
			}
			cfg.TrustedProxyCIDRs = append(cfg.TrustedProxyCIDRs, network)
		}
	}
	if cfg.Environment == Production && len(cfg.TrustedProxyCIDRs) == 0 {
		return Config{}, fmt.Errorf("MCPASTE_TRUSTED_PROXY_CIDRS is required in production")
	}
	return cfg, nil
}

func nonEmpty(lookup LookupEnv, key string) (string, bool) {
	value, ok := lookup(key)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func validDatabaseURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		return false
	}
	return strings.Trim(parsed.Path, "/") != ""
}

func validateHTTPAddr(value string) error {
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("must be host:port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("port must be an integer from 1 to 65535")
	}
	return nil
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("must be debug, info, warn, or error")
	}
}
```

- [ ] **Step 4: Make configuration tests green**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go
go test -race ./internal/config
```

Expected: PASS. Error tests prove invalid database and key values are not echoed.

- [ ] **Step 5: Write pool tests first**

Create `internal/database/pool_test.go` with:

```go
package database

import (
	"context"
	"strings"
	"testing"
)

func TestOpenRejectsMalformedURLWithoutEcho(t *testing.T) {
	marker := "database-url-secret-marker"
	_, err := Open(context.Background(), marker)
	if err == nil {
		t.Fatal("Open() error = nil")
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatal("Open() error echoed the database URL")
	}
}
```

- [ ] **Step 6: Run the pool test red**

```bash
go test ./internal/database
```

Expected: FAIL because `Open` is undefined.

- [ ] **Step 7: Create the small pgx pool boundary**

Create `internal/database/pool.go` with:

```go
package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("parse database configuration")
	}
	config.MaxConns = 10
	config.MinConns = 0
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 15 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, errors.New("create database pool")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, errors.New("connect to database")
	}
	return pool, nil
}

func Ready(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("database unavailable")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return pool.Ping(pingCtx)
}
```

- [ ] **Step 8: Verify pool creation and readiness against local PostgreSQL**

Replace the import block in `internal/database/pool_test.go` only now, when `os` is first used:

```go
import (
	"context"
	"os"
	"strings"
	"testing"
)
```

Then append this second test to `internal/database/pool_test.go`:

```go
func TestOpenAndReadyIntegration(t *testing.T) {
	databaseURL := os.Getenv("MCPASTE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MCPASTE_TEST_DATABASE_URL is not set")
	}
	pool, err := Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer pool.Close()
	if err := Ready(context.Background(), pool); err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
}
```

```bash
gofmt -w internal/database/pool.go internal/database/pool_test.go
export MCPASTE_TEST_DATABASE_URL='postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable'
go test -race ./internal/database
go vet ./internal/config ./internal/database
```

Expected: all commands pass.

- [ ] **Step 9: Commit configuration and readiness plumbing**

```bash
git add internal/config internal/database/pool.go internal/database/pool_test.go
git commit -m "feat: add PostgreSQL server configuration"
```

Expected: one commit preserving all Foundation environment, log-level, and address behavior while adding the stateful boundary.

## Task 6: Freeze identity domain types, validation, and store signatures

**Files:**

- Create: `internal/identity/model.go`
- Create: `internal/identity/dto_test.go`
- Create: `internal/identity/dto.go`
- Create: `internal/identity/naming_test.go`
- Create: `internal/identity/naming.go`
- Create: `internal/identity/store.go`

- [ ] **Step 1: Create all shared domain types before any consumer**

Create `internal/identity/model.go` with:

```go
package identity

import (
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/secure"
)

const MaxJSONBodyBytes int64 = 4096
const PairingLifetime = 5 * time.Minute
const ClaimLifetime = 5 * time.Minute
const IdempotencyLifetime = 24 * time.Hour
const EventLifetime = 35 * 24 * time.Hour
const PairingMetadataLifetime = 24 * time.Hour
const RateLimitRetention = 24 * time.Hour

var ErrInvalid = errors.New("invalid identity input")
var ErrUnauthorized = errors.New("unauthorized")
var ErrForbidden = errors.New("forbidden")
var ErrNotFound = errors.New("not found")
var ErrIdempotencyConflict = errors.New("idempotency conflict")
var ErrPairingPending = errors.New("pairing pending")
var ErrPairingApproved = errors.New("pairing already approved")
var ErrPairingExpired = errors.New("pairing expired")
var ErrInvalidClaim = errors.New("invalid claim")
var ErrInvalidRecovery = errors.New("invalid recovery")
var ErrRateLimited = errors.New("rate limited")

type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type Device struct {
	ID          string
	DisplayName string
	Platform    string
	Role        string
	CreatedAt   time.Time
	IsCurrent   bool
}

type Principal struct {
	WorkspaceID string
	DeviceID    string
	Scope       string
}

type CredentialRecord struct {
	DeviceID  string
	Locator   string
	Scope     string
	Hash      []byte
	CreatedAt time.Time
}

type RecoveryRecord struct {
	WorkspaceID string
	Locator     string
	Verifier    secure.RecoveryVerifier
	CreatedAt   time.Time
	RotatedAt   time.Time
}

type Pairing struct {
	ID                 string
	ShortCode          string
	ClaimHash           []byte
	ProposedName        string
	Platform            string
	RequestedScope      string
	WorkspaceID         string
	ApprovedByDeviceID  string
	DeviceID            string
	CreatedAt           time.Time
	ExpiresAt           time.Time
	ApprovedAt          time.Time
	ClaimExpiresAt      time.Time
	ClaimedAt           time.Time
	ClaimInvalidatedAt time.Time
	Grant               secure.Envelope
	MetadataPurgeAt     time.Time
}

type StoredResponse struct {
	Status      int
	ContentType string
	Envelope    secure.Envelope
}

type IdempotencyRecord struct {
	ScopeID     string
	Operation   string
	KeyHash     []byte
	WorkspaceID string
	RequestHash []byte
	Response    StoredResponse
	CreatedAt   time.Time
	ExpiresAt   time.Time
	Expired     bool
}

type RateDecision struct {
	Allowed    bool
	RetryAfter time.Duration
}

type RateRule struct {
	Scope  string
	Limit  int
	Window time.Duration
}

type CleanupResult struct {
	RevokedDevices     int64
	PairingRows        int64
	IdempotencyRows    int64
	EventRows          int64
	RateLimitRows      int64
}
```

- [ ] **Step 2: Write exact response DTO and UTC-second JSON tests**

Create `internal/identity/dto_test.go` with:

```go
package identity

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestWorkspaceGrantJSONHasExactDeviceFields(t *testing.T) {
	value := WorkspaceGrant{
		WorkspaceID: "00000000-0000-4000-8000-000000000101",
		Device: GrantDevice{
			ID:          "00000000-0000-4000-8000-000000000201",
			DisplayName: "MacBook Pro",
			Platform:    "macos",
			Role:        "full",
		},
		Credentials: []CredentialResponse{},
	}
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := []byte(`{"workspace_id":"00000000-0000-4000-8000-000000000101","device":{"id":"00000000-0000-4000-8000-000000000201","display_name":"MacBook Pro","platform":"macos","role":"full"},"credentials":[]}`)
	if !bytes.Equal(got, want) {
		t.Fatal("WorkspaceGrant JSON field set differs")
	}
}

func TestDeviceSummaryJSONAlwaysIncludesCurrentAndUTCSeconds(t *testing.T) {
	value := deviceSummary(Device{
		ID:          "00000000-0000-4000-8000-000000000201",
		DisplayName: "MacBook Pro",
		Platform:    "macos",
		Role:        "full",
		CreatedAt:   time.Date(2026, 8, 12, 21, 0, 0, 987654321, time.FixedZone("KST", 9*60*60)),
		IsCurrent:   false,
	})
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := []byte(`{"id":"00000000-0000-4000-8000-000000000201","display_name":"MacBook Pro","platform":"macos","role":"full","created_at":"2026-08-12T12:00:00Z","is_current":false}`)
	if !bytes.Equal(got, want) {
		t.Fatal("DeviceSummary JSON field set or timestamp precision differs")
	}
}

func TestPairingResponseTimesUseUTCSeconds(t *testing.T) {
	const pairingIDForDTOTest = "00000000-0000-4000-8000-000000000301"
	value := PairingCreateResponse{
		PairingID:  pairingIDForDTOTest,
		QRPayload:  "mcpaste-pairing:00000000-0000-4000-8000-000000000301",
		ShortCode:  "23456789",
		ClaimSecret: "test-value-not-a-credential",
		ExpiresAt:  wireTime(time.Date(2026, 8, 12, 12, 5, 0, 999, time.UTC)),
	}
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := []byte(`{"pairing_id":"00000000-0000-4000-8000-000000000301","qr_payload":"mcpaste-pairing:00000000-0000-4000-8000-000000000301","short_code":"23456789","claim_secret":"test-value-not-a-credential","expires_at":"2026-08-12T12:05:00Z"}`)
	if !bytes.Equal(got, want) {
		t.Fatal("PairingCreateResponse JSON field set or timestamp precision differs")
	}
	approval, err := json.Marshal(ApprovalResponse{
		PairingID: pairingIDForDTOTest,
		Status: "approved",
		ClaimExpiresAt: wireTime(time.Date(2026, 8, 12, 12, 10, 0, 999, time.UTC)),
	})
	if err != nil {
		t.Fatalf("Marshal() approval error = %v", err)
	}
	wantApproval := []byte(`{"pairing_id":"00000000-0000-4000-8000-000000000301","status":"approved","claim_expires_at":"2026-08-12T12:10:00Z"}`)
	if !bytes.Equal(approval, wantApproval) {
		t.Fatal("ApprovalResponse JSON field set or timestamp precision differs")
	}
	claimExpiry := wireTime(time.Date(2026, 8, 12, 12, 10, 0, 999, time.UTC))
	details, err := json.Marshal(PairingDetails{
		PairingID: pairingIDForDTOTest, ProposedName: "Build Host", Platform: "linux",
		RequestedScope: "connector", Status: "approved",
		ExpiresAt: wireTime(time.Date(2026, 8, 12, 12, 5, 0, 999, time.UTC)),
		ClaimExpiresAt: &claimExpiry,
	})
	if err != nil {
		t.Fatalf("Marshal() details error = %v", err)
	}
	wantDetails := []byte(`{"pairing_id":"00000000-0000-4000-8000-000000000301","proposed_name":"Build Host","platform":"linux","requested_scope":"connector","status":"approved","expires_at":"2026-08-12T12:05:00Z","claim_expires_at":"2026-08-12T12:10:00Z"}`)
	if !bytes.Equal(details, wantDetails) {
		t.Fatal("PairingDetails JSON field set or timestamp precision differs")
	}
}
```

- [ ] **Step 3: Run DTO tests red**

```bash
go test ./internal/identity -run 'TestWorkspaceGrantJSON|TestDeviceSummaryJSON|TestPairingResponseTimes'
```

Expected: FAIL because the wire DTOs and mappers are undefined.

- [ ] **Step 4: Define wire-only DTOs and mappers**

Create `internal/identity/dto.go` with:

```go
package identity

import "time"

type CredentialResponse struct {
	Kind  string `json:"kind"`
	Token string `json:"token"`
}

type GrantDevice struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Platform    string `json:"platform"`
	Role        string `json:"role"`
}

type WorkspaceGrant struct {
	WorkspaceID  string               `json:"workspace_id"`
	Device       GrantDevice          `json:"device"`
	Credentials  []CredentialResponse `json:"credentials"`
	RecoveryCode string               `json:"recovery_code,omitempty"`
}

type DeviceSummary struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	Platform    string    `json:"platform"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	IsCurrent   bool      `json:"is_current"`
}

type PairingCreateResponse struct {
	PairingID   string    `json:"pairing_id"`
	QRPayload   string    `json:"qr_payload"`
	ShortCode   string    `json:"short_code"`
	ClaimSecret string    `json:"claim_secret"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type PairingDetails struct {
	PairingID      string     `json:"pairing_id"`
	ProposedName   string     `json:"proposed_name"`
	Platform       string     `json:"platform"`
	RequestedScope string     `json:"requested_scope"`
	Status         string     `json:"status"`
	ExpiresAt      time.Time  `json:"expires_at"`
	ClaimExpiresAt *time.Time `json:"claim_expires_at,omitempty"`
}

type ApprovalResponse struct {
	PairingID      string    `json:"pairing_id"`
	Status         string    `json:"status"`
	ClaimExpiresAt time.Time `json:"claim_expires_at"`
}

func grantDevice(device Device) GrantDevice {
	return GrantDevice{
		ID: device.ID, DisplayName: device.DisplayName, Platform: device.Platform, Role: device.Role,
	}
}

func deviceSummary(device Device) DeviceSummary {
	return DeviceSummary{
		ID: device.ID, DisplayName: device.DisplayName, Platform: device.Platform, Role: device.Role,
		CreatedAt: wireTime(device.CreatedAt), IsCurrent: device.IsCurrent,
	}
}

func wireTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Second)
}
```

- [ ] **Step 5: Make DTO tests green**

```bash
gofmt -w internal/identity/model.go internal/identity/dto.go internal/identity/dto_test.go
go test -race ./internal/identity -run 'TestWorkspaceGrantJSON|TestDeviceSummaryJSON|TestPairingResponseTimes'
```

Expected: PASS. `Device` remains an internal persistence model with no JSON tags; only `GrantDevice`, `DeviceSummary`, and the other DTOs cross the HTTP boundary.

- [ ] **Step 6: Write name validation and suffix tests**

Create `internal/identity/naming_test.go` with:

```go
package identity

import (
	"strings"
	"testing"
)

func TestNormalizeDisplayName(t *testing.T) {
	got, err := NormalizeDisplayName("  MacBook Pro  ")
	if err != nil || got != "MacBook Pro" {
		t.Fatalf("NormalizeDisplayName() = %q, %v", got, err)
	}
	for _, value := range []string{"", "   ", "bad\nname", "bad\u007fname", strings.Repeat("가", 81)} {
		if _, err := NormalizeDisplayName(value); err == nil {
			t.Fatalf("NormalizeDisplayName(%q) error = nil", value)
		}
	}
}

func TestDisplayNameCandidateUsesSmallestSuffixAndLengthLimit(t *testing.T) {
	if got := DisplayNameCandidate("MacBook Pro", 1); got != "MacBook Pro" {
		t.Fatalf("attempt 1 = %q", got)
	}
	if got := DisplayNameCandidate("MacBook Pro", 2); got != "MacBook Pro (2)" {
		t.Fatalf("attempt 2 = %q", got)
	}
	got := DisplayNameCandidate(strings.Repeat("가", 80), 12)
	if len([]rune(got)) != 80 || !strings.HasSuffix(got, " (12)") {
		t.Fatalf("length-limited candidate = %q (%d runes)", got, len([]rune(got)))
	}
}
```

- [ ] **Step 7: Run naming tests red**

```bash
go test ./internal/identity -run 'TestNormalize|TestDisplayName'
```

Expected: FAIL because both naming functions are undefined.

- [ ] **Step 8: Write exact name validation and candidate generation**

Create `internal/identity/naming.go` with:

```go
package identity

import (
	"fmt"
	"strings"
	"unicode"
)

const maxDisplayNameRunes = 80

func NormalizeDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) < 1 || len(runes) > maxDisplayNameRunes {
		return "", ErrInvalid
	}
	for _, current := range runes {
		if unicode.IsControl(current) {
			return "", ErrInvalid
		}
	}
	return value, nil
}

func DisplayNameCandidate(base string, attempt int) string {
	if attempt <= 1 {
		return base
	}
	suffix := fmt.Sprintf(" (%d)", attempt)
	baseRunes := []rune(base)
	maximumBase := maxDisplayNameRunes - len([]rune(suffix))
	if len(baseRunes) > maximumBase {
		baseRunes = baseRunes[:maximumBase]
	}
	return strings.TrimSpace(string(baseRunes)) + suffix
}
```

- [ ] **Step 9: Make naming tests green**

```bash
gofmt -w internal/identity/model.go internal/identity/dto.go internal/identity/dto_test.go internal/identity/naming.go internal/identity/naming_test.go
go test -race ./internal/identity -run 'TestNormalize|TestDisplayName'
```

Expected: PASS.

- [ ] **Step 10: Define the transaction-oriented repository contract**

Create `internal/identity/store.go` with:

```go
package identity

import (
	"context"
	"time"
)

type Store interface {
	WithinTx(context.Context, func(TxStore) error) error
	Authenticate(context.Context, string, string, []byte, time.Time) (Principal, error)
	ConsumeRateLimit(context.Context, RateRule, []byte, time.Time) (RateDecision, error)
}

type TxStore interface {
	GetIdempotency(context.Context, string, string, []byte) (IdempotencyRecord, error)
	DeleteIdempotency(context.Context, string, string, []byte) error
	PutIdempotency(context.Context, IdempotencyRecord) error
	InsertWorkspace(context.Context, string, time.Time) error
	InsertDevice(context.Context, string, Device) error
	InsertCredential(context.Context, string, CredentialRecord) error
	GetRecovery(context.Context, string, string) (RecoveryRecord, error)
	PutRecovery(context.Context, string, RecoveryRecord) error
}
```

This is the deliberately minimal compile boundary needed by Task 7. For idempotency methods, the first two strings after context are `scopeID` and `operation`; for established-workspace methods, the first string after context is `workspaceID`. `GetIdempotency` locks and returns a row regardless of logical expiry so the service can replace an expired row transactionally; `DeleteIdempotency` performs that replacement step. `ConsumeRateLimit` operates on an irreversible subject hash. Task 8 replaces this file with the complete pairing/device/event interface before adding implementations, so no temporary method or panic stub is needed.

- [ ] **Step 11: Add deterministic UUID generation to the existing random seam**

Append this exact function and imports to `internal/secure/random.go`:

```go
func NewUUID(source Random) (string, error) {
	value, err := randomBytes(source, 16)
	if err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
```

Add `"encoding/hex"` to `internal/secure/random.go`. Add this test to `internal/secure/credential_test.go`:

```go
func TestNewUUIDSetsVersionAndVariant(t *testing.T) {
	got, err := NewUUID(bytes.NewReader(bytes.Repeat([]byte{0xff}, 16)))
	if err != nil {
		t.Fatalf("NewUUID() error = %v", err)
	}
	if got != "ffffffff-ffff-4fff-bfff-ffffffffffff" {
		t.Fatalf("NewUUID() = %q", got)
	}
}
```

- [ ] **Step 12: Verify type and signature consistency**

```bash
gofmt -w internal/identity internal/secure/random.go internal/secure/credential_test.go
go test -race ./internal/identity ./internal/secure
go vet ./internal/identity ./internal/secure
```

Expected: all commands pass. Check that every wire DTO field name exactly matches the API table and that internal `Device` values are mapped before marshaling.

- [ ] **Step 13: Commit the domain boundary**

```bash
git add internal/identity internal/secure/random.go internal/secure/credential_test.go
git commit -m "feat: define identity domain contracts"
```

Expected: one commit containing types, naming, store signatures, and UUID generation only.

## Task 7: Write the PostgreSQL repository core

**Files:**

- Create: `internal/identity/postgres/store_integration_test.go`
- Create: `internal/identity/postgres/store.go`
- Create: `internal/identity/postgres/auth.go`
- Create: `internal/identity/postgres/idempotency.go`
- Create: `internal/identity/postgres/rate_limit.go`
- Create: `internal/identity/postgres/onboarding.go`

- [ ] **Step 1: Correct `InsertDevice` to return the allocated name**

In `internal/identity/store.go`, replace the `InsertDevice` signature with:

```go
	InsertDevice(context.Context, string, Device) (Device, error)
```

No other signature changes in this task.

- [ ] **Step 2: Write repository core integration tests first**

Create `internal/identity/postgres/store_integration_test.go` with:

```go
package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/1yoouoo/mcpaste/internal/testdb"
)

const workspaceOne = "00000000-0000-4000-8000-000000000101"
const workspaceTwo = "00000000-0000-4000-8000-000000000102"
const deviceOne = "00000000-0000-4000-8000-000000000201"

func TestWorkspaceScopedCredentialAuthentication(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	hash := bytes.Repeat([]byte{0x41}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		device, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now,
		})
		if err != nil || device.DisplayName != "MacBook Pro" {
			t.Fatalf("InsertDevice() = %#v, %v", device, err)
		}
		return tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{
			DeviceID: deviceOne, Locator: "AAAAAAAAAAAAAAAAAAAAAA", Scope: "full", Hash: hash, CreatedAt: now,
		})
	})
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	principal, err := store.Authenticate(ctx, workspaceOne, "AAAAAAAAAAAAAAAAAAAAAA", hash, now)
	if err != nil || principal.WorkspaceID != workspaceOne || principal.DeviceID != deviceOne || principal.Scope != "full" {
		t.Fatalf("Authenticate() = %#v, %v", principal, err)
	}
	if _, err := store.Authenticate(ctx, workspaceTwo, "AAAAAAAAAAAAAAAAAAAAAA", hash, now); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("cross-workspace Authenticate() error = %v", err)
	}
	if _, err := store.Authenticate(ctx, workspaceOne, "AAAAAAAAAAAAAAAAAAAAAA", bytes.Repeat([]byte{0x42}, 32), now); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("wrong-secret Authenticate() error = %v", err)
	}
}

func TestDeviceNameSuffixIsWorkspaceLocalAndCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		if err := tx.InsertWorkspace(ctx, workspaceTwo, now); err != nil {
			return err
		}
		first, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: deviceOne, DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil || first.DisplayName != "MacBook Pro" {
			t.Fatalf("first device = %#v, %v", first, err)
		}
		second, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: "00000000-0000-4000-8000-000000000202", DisplayName: "macbook pro", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil || second.DisplayName != "macbook pro (2)" {
			t.Fatalf("second device = %#v, %v", second, err)
		}
		other, err := tx.InsertDevice(ctx, workspaceTwo, identity.Device{ID: "00000000-0000-4000-8000-000000000203", DisplayName: "MACBOOK PRO", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil || other.DisplayName != "MACBOOK PRO" {
			t.Fatalf("other workspace device = %#v, %v", other, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("device suffix transaction: %v", err)
	}
}

func TestIdempotencyAndRateLimitPersistence(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	keyHash := bytes.Repeat([]byte{0x51}, 32)
	requestHash := bytes.Repeat([]byte{0x52}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		return tx.PutIdempotency(ctx, identity.IdempotencyRecord{
			ScopeID: "public", Operation: "workspace.create", KeyHash: keyHash, RequestHash: requestHash,
			Response: identity.StoredResponse{Status: 201, ContentType: "application/json", Envelope: secure.Envelope{
				KeyID: "test-key", Nonce: bytes.Repeat([]byte{0x53}, 12), Ciphertext: []byte{0x54},
			}},
		})
	})
	if err != nil {
		t.Fatalf("PutIdempotency() error = %v", err)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		got, err := tx.GetIdempotency(ctx, "public", "workspace.create", keyHash)
		if err != nil || !bytes.Equal(got.RequestHash, requestHash) || got.Response.Status != 201 {
			t.Fatalf("GetIdempotency() metadata mismatch: err=%v status=%d", err, got.Response.Status)
		}
		if got.ScopeID != "public" || got.Expired || got.ExpiresAt.Sub(got.CreatedAt) != identity.IdempotencyLifetime {
			t.Fatalf("idempotency lifetime metadata mismatch: scope=%q expired=%v lifetime=%v", got.ScopeID, got.Expired, got.ExpiresAt.Sub(got.CreatedAt))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("idempotency lookup transaction: %v", err)
	}
	rule := identity.RateRule{Scope: "workspace.create", Limit: 2, Window: time.Minute}
	for call := 1; call <= 3; call++ {
		decision, err := store.ConsumeRateLimit(ctx, rule, bytes.Repeat([]byte{0x61}, 32), now)
		if err != nil {
			t.Fatalf("ConsumeRateLimit() error = %v", err)
		}
		if decision.Allowed != (call <= 2) {
			t.Fatalf("call %d Allowed = %v", call, decision.Allowed)
		}
	}
}

func TestRateLimitFixedWindowResetAndRetention(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	windowStart := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	rule := identity.RateRule{Scope: "pairing.lookup", Limit: 2, Window: 5 * time.Minute}
	subjectHash := bytes.Repeat([]byte{0x62}, 32)

	for call := 1; call <= 2; call++ {
		decision, err := store.ConsumeRateLimit(ctx, rule, subjectHash, windowStart)
		if err != nil {
			t.Fatalf("initial ConsumeRateLimit() call %d error = %v", call, err)
		}
		if !decision.Allowed {
			t.Fatalf("initial call %d was denied", call)
		}
	}

	boundary := windowStart.Add(rule.Window)
	decision, err := store.ConsumeRateLimit(ctx, rule, subjectHash, boundary)
	if err != nil {
		t.Fatalf("boundary ConsumeRateLimit() error = %v", err)
	}
	if !decision.Allowed {
		t.Fatal("first request in reset window was denied")
	}

	var count int
	var storedStart time.Time
	var storedExpires time.Time
	if err := pool.QueryRow(ctx, `
select request_count, window_started_at, expires_at
from rate_limit_buckets
where scope = $1 and subject_hash = $2`, rule.Scope, subjectHash).Scan(
		&count, &storedStart, &storedExpires,
	); err != nil {
		t.Fatalf("inspect reset rate limit: %v", err)
	}
	wantExpires := boundary.Add(rule.Window + identity.RateLimitRetention)
	if count != 1 {
		t.Fatalf("reset request_count = %d", count)
	}
	if !storedStart.Equal(boundary) {
		t.Fatal("reset window_started_at differs from boundary")
	}
	if !storedExpires.Equal(wantExpires) {
		t.Fatal("reset expires_at differs from window end plus retention")
	}
}

func TestIdempotencyScopeIsolation(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	keyHash := bytes.Repeat([]byte{0x71}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		if err := tx.InsertWorkspace(ctx, workspaceTwo, now); err != nil {
			return err
		}
		for _, item := range []struct {
			scopeID     string
			requestByte byte
		}{
			{scopeID: workspaceOne, requestByte: 0x72},
			{scopeID: workspaceTwo, requestByte: 0x73},
		} {
			if err := tx.PutIdempotency(ctx, identity.IdempotencyRecord{
				ScopeID: item.scopeID, Operation: "device.rename", KeyHash: keyHash,
				WorkspaceID: item.scopeID, RequestHash: bytes.Repeat([]byte{item.requestByte}, 32),
				Response: identity.StoredResponse{Status: 200, ContentType: "application/json", Envelope: secure.Envelope{
					KeyID: "test-key", Nonce: bytes.Repeat([]byte{item.requestByte + 1}, 12), Ciphertext: []byte{item.requestByte + 2},
				}},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed scoped idempotency: %v", err)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		first, err := tx.GetIdempotency(ctx, workspaceOne, "device.rename", keyHash)
		if err != nil {
			return err
		}
		second, err := tx.GetIdempotency(ctx, workspaceTwo, "device.rename", keyHash)
		if err != nil {
			return err
		}
		if bytes.Equal(first.RequestHash, second.RequestHash) {
			t.Fatal("workspace-scoped idempotency records were not independent")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read scoped idempotency: %v", err)
	}
}
```

- [ ] **Step 3: Run repository tests red**

```bash
export MCPASTE_TEST_DATABASE_URL='postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable'
go test ./internal/identity/postgres
```

Expected: FAIL because `New` and every repository method are undefined.

- [ ] **Step 4: Create the pool and transaction adapter**

Create `internal/identity/postgres/store.go` with:

```go
package postgres

import (
	"context"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

type txStore struct {
	tx pgx.Tx
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) WithinTx(ctx context.Context, fn func(identity.TxStore) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(&txStore{tx: tx})
	})
}
```

- [ ] **Step 5: Write workspace-scoped authentication**

Create `internal/identity/postgres/auth.go` with:

```go
package postgres

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
)

func (s *Store) Authenticate(ctx context.Context, workspaceID, locator string, presentedHash []byte, now time.Time) (identity.Principal, error) {
	var principal identity.Principal
	var storedHash []byte
	err := s.pool.QueryRow(ctx, `
select c.workspace_id::text, c.device_id::text, c.scope, c.secret_hash
from credentials c
join devices d on d.workspace_id = c.workspace_id and d.id = c.device_id
where c.workspace_id = $1::uuid and c.token_id = $2
  and c.revoked_at is null and d.revoked_at is null`, workspaceID, locator).Scan(
		&principal.WorkspaceID, &principal.DeviceID, &principal.Scope, &storedHash,
	)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && subtle.ConstantTimeCompare(storedHash, presentedHash) != 1 {
		return identity.Principal{}, identity.ErrUnauthorized
	}
	if err != nil {
		return identity.Principal{}, err
	}
	_, err = s.pool.Exec(ctx, `
update credentials set last_used_at = $3
where workspace_id = $1::uuid and token_id = $2 and revoked_at is null`, workspaceID, locator, now)
	if err != nil {
		return identity.Principal{}, err
	}
	return principal, nil
}
```

- [ ] **Step 6: Write encrypted idempotency persistence**

Create `internal/identity/postgres/idempotency.go` with:

```go
package postgres

import (
	"context"
	"errors"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
)

func (s *txStore) GetIdempotency(ctx context.Context, scopeID, operation string, keyHash []byte) (identity.IdempotencyRecord, error) {
	var record identity.IdempotencyRecord
	var workspaceID *string
	err := s.tx.QueryRow(ctx, `
select scope_id, operation, key_hash, workspace_id::text, request_hash,
       response_status, response_content_type, response_key_id,
       response_nonce, response_ciphertext, created_at, expires_at,
       expires_at <= clock_timestamp()
from idempotency_records
where scope_id = $1 and operation = $2 and key_hash = $3
for update`, scopeID, operation, keyHash).Scan(
		&record.ScopeID, &record.Operation, &record.KeyHash, &workspaceID, &record.RequestHash,
		&record.Response.Status, &record.Response.ContentType, &record.Response.Envelope.KeyID,
		&record.Response.Envelope.Nonce, &record.Response.Envelope.Ciphertext,
		&record.CreatedAt, &record.ExpiresAt, &record.Expired,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.IdempotencyRecord{}, identity.ErrNotFound
	}
	if err != nil {
		return identity.IdempotencyRecord{}, err
	}
	if workspaceID != nil {
		record.WorkspaceID = *workspaceID
	}
	return record, nil
}

func (s *txStore) DeleteIdempotency(ctx context.Context, scopeID, operation string, keyHash []byte) error {
	command, err := s.tx.Exec(ctx, `
delete from idempotency_records
where scope_id = $1 and operation = $2 and key_hash = $3`, scopeID, operation, keyHash)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return identity.ErrNotFound
	}
	return nil
}

func (s *txStore) PutIdempotency(ctx context.Context, record identity.IdempotencyRecord) error {
	var workspaceID any
	if record.WorkspaceID != "" {
		workspaceID = record.WorkspaceID
	}
	_, err := s.tx.Exec(ctx, `
insert into idempotency_records(
    scope_id, operation, key_hash, workspace_id, request_hash,
    response_status, response_content_type, response_key_id,
    response_nonce, response_ciphertext, created_at, expires_at
) select $1, $2, $3, $4::uuid, $5, $6, $7, $8, $9, $10,
         stamp.created_at, stamp.created_at + interval '24 hours'
  from (select clock_timestamp() as created_at) stamp`,
		record.ScopeID, record.Operation, record.KeyHash, workspaceID, record.RequestHash,
		record.Response.Status, record.Response.ContentType, record.Response.Envelope.KeyID,
		record.Response.Envelope.Nonce, record.Response.Envelope.Ciphertext,
	)
	return err
}
```

- [ ] **Step 7: Write exact fixed-window rate limiting**

Create `internal/identity/postgres/rate_limit.go` with:

```go
package postgres

import (
	"context"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

func (s *Store) ConsumeRateLimit(ctx context.Context, rule identity.RateRule, subjectHash []byte, now time.Time) (identity.RateDecision, error) {
	resetBefore := now.Add(-rule.Window)
	rowExpires := now.Add(rule.Window + identity.RateLimitRetention)
	var count int
	var started time.Time
	err := s.pool.QueryRow(ctx, `
insert into rate_limit_buckets(scope, subject_hash, window_started_at, request_count, expires_at)
values ($1, $2, $3, 1, $4)
on conflict (scope, subject_hash) do update set
    window_started_at = case
        when rate_limit_buckets.window_started_at <= $5 then excluded.window_started_at
        else rate_limit_buckets.window_started_at
    end,
    request_count = case
        when rate_limit_buckets.window_started_at <= $5 then 1
        else rate_limit_buckets.request_count + 1
    end,
    expires_at = case
        when rate_limit_buckets.window_started_at <= $5 then excluded.expires_at
        else rate_limit_buckets.expires_at
    end
returning request_count, window_started_at`,
		rule.Scope, subjectHash, now, rowExpires, resetBefore,
	).Scan(&count, &started)
	if err != nil {
		return identity.RateDecision{}, err
	}
	if count <= rule.Limit {
		return identity.RateDecision{Allowed: true}, nil
	}
	retry := started.Add(rule.Window).Sub(now)
	if retry < time.Second {
		retry = time.Second
	}
	return identity.RateDecision{Allowed: false, RetryAfter: retry}, nil
}
```

- [ ] **Step 8: Write workspace, device, credential, and recovery row operations**

Create `internal/identity/postgres/onboarding.go` with:

```go
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
)

func (s *txStore) InsertWorkspace(ctx context.Context, workspaceID string, createdAt time.Time) error {
	_, err := s.tx.Exec(ctx, "insert into workspaces(id, created_at) values ($1::uuid, $2)", workspaceID, createdAt)
	return err
}

func (s *txStore) InsertDevice(ctx context.Context, workspaceID string, device identity.Device) (identity.Device, error) {
	if _, err := s.tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1, 0))", workspaceID); err != nil {
		return identity.Device{}, err
	}
	for attempt := 1; attempt <= 9999; attempt++ {
		candidate := identity.DisplayNameCandidate(device.DisplayName, attempt)
		var exists bool
		if err := s.tx.QueryRow(ctx, `
select exists(select 1 from devices where workspace_id = $1::uuid and lower(display_name) = lower($2))`,
			workspaceID, candidate).Scan(&exists); err != nil {
			return identity.Device{}, err
		}
		if exists {
			continue
		}
		_, err := s.tx.Exec(ctx, `
insert into devices(id, workspace_id, display_name, platform, role, created_at)
values ($1::uuid, $2::uuid, $3, $4, $5, $6)`,
			device.ID, workspaceID, candidate, device.Platform, device.Role, device.CreatedAt,
		)
		if err != nil {
			return identity.Device{}, err
		}
		device.DisplayName = candidate
		return device, nil
	}
	return identity.Device{}, identity.ErrInvalid
}

func (s *txStore) InsertCredential(ctx context.Context, workspaceID string, record identity.CredentialRecord) error {
	_, err := s.tx.Exec(ctx, `
insert into credentials(workspace_id, device_id, token_id, scope, secret_hash, created_at)
values ($1::uuid, $2::uuid, $3, $4, $5, $6)`,
		workspaceID, record.DeviceID, record.Locator, record.Scope, record.Hash, record.CreatedAt,
	)
	return err
}

func (s *txStore) GetRecovery(ctx context.Context, workspaceID, locator string) (identity.RecoveryRecord, error) {
	var record identity.RecoveryRecord
	err := s.tx.QueryRow(ctx, `
select workspace_id::text, locator, salt, verifier,
       argon_version, argon_time, argon_memory_kib, argon_threads,
       created_at, rotated_at
from recovery_verifiers
where workspace_id = $1::uuid and locator = $2
for update`, workspaceID, locator).Scan(
		&record.WorkspaceID, &record.Locator,
		&record.Verifier.Salt, &record.Verifier.Hash,
		&record.Verifier.Version, &record.Verifier.Time,
		&record.Verifier.MemoryKiB, &record.Verifier.Threads,
		&record.CreatedAt, &record.RotatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.RecoveryRecord{}, identity.ErrInvalidRecovery
	}
	return record, err
}

func (s *txStore) PutRecovery(ctx context.Context, workspaceID string, record identity.RecoveryRecord) error {
	_, err := s.tx.Exec(ctx, `
insert into recovery_verifiers(
    workspace_id, locator, salt, verifier, argon_version,
    argon_time, argon_memory_kib, argon_threads, created_at, rotated_at
) values ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10)
on conflict (workspace_id) do update set
    locator = excluded.locator,
    salt = excluded.salt,
    verifier = excluded.verifier,
    argon_version = excluded.argon_version,
    argon_time = excluded.argon_time,
    argon_memory_kib = excluded.argon_memory_kib,
    argon_threads = excluded.argon_threads,
    rotated_at = excluded.rotated_at`,
		workspaceID, record.Locator, record.Verifier.Salt, record.Verifier.Hash,
		record.Verifier.Version, record.Verifier.Time, record.Verifier.MemoryKiB,
		record.Verifier.Threads, record.CreatedAt, record.RotatedAt,
	)
	return err
}

```

- [ ] **Step 9: Make repository core tests green**

```bash
gofmt -w internal/identity/store.go internal/identity/postgres
export MCPASTE_TEST_DATABASE_URL='postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable'
go test -race ./internal/identity/postgres -run 'TestWorkspaceScoped|TestDeviceName|TestIdempotency|TestRateLimit'
go vet ./internal/identity/postgres
```

Expected: PASS. The authentication test proves the SQL requires both workspace ID and locator. The fixed-window test crosses the boundary exactly, observes count 1, and compares PostgreSQL-returned `window_started_at` and `expires_at` with `Time.Equal`; expiry is exactly the new window end plus 24-hour retention.

- [ ] **Step 10: Commit the repository core**

```bash
git add internal/identity/store.go internal/identity/postgres
git commit -m "feat: add identity PostgreSQL repository core"
```

Expected: one commit containing scoped authentication, idempotency, rate limits, onboarding rows, and focused integration tests.

## Task 8: Complete pairing, devices, events, and cleanup persistence

**Files:**

- Modify: `internal/identity/store.go`
- Modify: `internal/identity/postgres/store.go`
- Create: `internal/identity/postgres/pairing.go`
- Create: `internal/identity/postgres/devices.go`
- Create: `internal/identity/postgres/cleanup.go`
- Modify: `internal/identity/postgres/store_integration_test.go`

- [ ] **Step 1: Append pairing replay and device isolation tests**

Append these tests to `internal/identity/postgres/store_integration_test.go`:

```go
func TestPairingClaimReplayReturnsSameEncryptedGrant(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	claimHash := bytes.Repeat([]byte{0x71}, 32)
	grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0x72}, 12), Ciphertext: []byte{0x73, 0x74}}
	pairingID := "00000000-0000-4000-8000-000000000301"
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "23456789", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: now, ExpiresAt: now.Add(identity.PairingLifetime),
			MetadataPurgeAt: now.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: "00000000-0000-4000-8000-000000000302", DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: now})
		if err != nil {
			return err
		}
		return tx.ApprovePairing(ctx, workspaceOne, pairingID, approver.ID, joining.ID, now, now.Add(identity.ClaimLifetime), grant, now.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime))
	})
	if err != nil {
		t.Fatalf("seed approved pairing: %v", err)
	}
	claim := func(claimHash []byte, claimAt time.Time) (identity.Pairing, error) {
		var pairing identity.Pairing
		err := store.WithinTx(ctx, func(tx identity.TxStore) error {
			var err error
			pairing, err = tx.LockPairingForClaim(ctx, pairingID, claimHash, claimAt)
			if err != nil {
				return err
			}
			return tx.MarkPairingClaimed(ctx, pairingID, claimAt)
		})
		return pairing, err
	}
	first, err := claim(claimHash, now)
	if err != nil {
		t.Fatalf("first claim = %v", err)
	}
	second, err := claim(claimHash, now.Add(time.Second))
	if err != nil {
		t.Fatalf("second claim = %v", err)
	}
	if !bytes.Equal(first.Grant.Ciphertext, second.Grant.Ciphertext) || !bytes.Equal(first.Grant.Nonce, second.Grant.Nonce) {
		t.Fatal("claim replay changed encrypted grant")
	}
	if _, err := claim(bytes.Repeat([]byte{0x75}, 32), now); !errors.Is(err, identity.ErrInvalidClaim) {
		t.Fatalf("wrong claim error = %v", err)
	}
}

func TestApprovedPairingDetailsExpireWhileClaimRemainsValid(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	createdAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	approvedAt := createdAt.Add(4 * time.Minute)
	detailsAt := createdAt.Add(identity.PairingLifetime + time.Second)
	pairingID := "00000000-0000-4000-8000-000000000331"
	joiningID := "00000000-0000-4000-8000-000000000332"
	claimHash := bytes.Repeat([]byte{0x76}, 32)
	grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0x77}, 12), Ciphertext: []byte{0x78}}
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, createdAt); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "2345678D", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: createdAt, ExpiresAt: createdAt.Add(identity.PairingLifetime),
			MetadataPurgeAt: createdAt.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: joiningID, DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: approvedAt,
		})
		if err != nil {
			return err
		}
		return tx.ApprovePairing(
			ctx, workspaceOne, pairingID, approver.ID, joining.ID,
			approvedAt, approvedAt.Add(identity.ClaimLifetime), grant,
			approvedAt.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime),
		)
	})
	if err != nil {
		t.Fatalf("seed approved pairing: %v", err)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		if _, err := tx.GetPairingByID(ctx, workspaceOne, pairingID, detailsAt); !errors.Is(err, identity.ErrPairingExpired) {
			t.Fatalf("expired ID details error = %v", err)
		}
		if _, err := tx.GetPairingByShortCode(ctx, workspaceOne, "2345678D", detailsAt); !errors.Is(err, identity.ErrPairingExpired) {
			t.Fatalf("expired short-code details error = %v", err)
		}
		pairing, err := tx.LockPairingForClaim(ctx, pairingID, claimHash, detailsAt)
		if err != nil {
			return err
		}
		if !pairing.ClaimExpiresAt.Equal(approvedAt.Add(identity.ClaimLifetime)) {
			t.Fatal("private claim expiry differs from approval-relative window")
		}
		return tx.MarkPairingClaimed(ctx, pairingID, detailsAt)
	})
	if err != nil {
		t.Fatalf("expired-details/private-claim transaction: %v", err)
	}
}

func TestRenameListRevokeAndCrossWorkspaceRejection(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	hash := bytes.Repeat([]byte{0x81}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		if err := tx.InsertWorkspace(ctx, workspaceTwo, now); err != nil {
			return err
		}
		if _, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: deviceOne, DisplayName: "MacBook Pro (3)", Platform: "macos", Role: "full", CreatedAt: now}); err != nil {
			return err
		}
		if _, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: "00000000-0000-4000-8000-000000000204", DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now}); err != nil {
			return err
		}
		return tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{DeviceID: deviceOne, Locator: "BBBBBBBBBBBBBBBBBBBBBB", Scope: "full", Hash: hash, CreatedAt: now})
	})
	if err != nil {
		t.Fatalf("seed devices: %v", err)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		renamed, err := tx.RenameDevice(ctx, workspaceOne, deviceOne, "MACBOOK PRO", now)
		if err != nil || renamed.DisplayName != "MACBOOK PRO (2)" {
			t.Fatalf("RenameDevice() = %#v, %v", renamed, err)
		}
		devices, err := tx.ListDevices(ctx, workspaceOne, deviceOne)
		if err != nil || len(devices) != 2 || !devices[0].IsCurrent {
			t.Fatalf("ListDevices() = %#v, %v", devices, err)
		}
		if _, err := tx.RenameDevice(ctx, workspaceTwo, deviceOne, "stolen", now); !errors.Is(err, identity.ErrNotFound) {
			t.Fatalf("cross-workspace rename error = %v", err)
		}
		if err := tx.RevokeDevice(ctx, workspaceOne, deviceOne, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("device administration transaction: %v", err)
	}
	if _, err := store.Authenticate(ctx, workspaceOne, "BBBBBBBBBBBBBBBBBBBBBB", hash, now); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("revoked auth error = %v", err)
	}
}

func TestRenameUsesFourthSuffixWhenSecondAndThirdBelongToOtherDevices(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	targetID := "00000000-0000-4000-8000-000000000205"
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		for _, device := range []identity.Device{
			{ID: deviceOne, DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now},
			{ID: "00000000-0000-4000-8000-000000000202", DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now},
			{ID: "00000000-0000-4000-8000-000000000203", DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now},
			{ID: targetID, DisplayName: "Target", Platform: "macos", Role: "full", CreatedAt: now},
		} {
			if _, err := tx.InsertDevice(ctx, workspaceOne, device); err != nil {
				return err
			}
		}
		renamed, err := tx.RenameDevice(ctx, workspaceOne, targetID, "MACBOOK PRO", now)
		if err != nil {
			return err
		}
		if renamed.DisplayName != "MACBOOK PRO (4)" {
			t.Fatalf("RenameDevice() display name = %q", renamed.DisplayName)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("fourth suffix transaction: %v", err)
	}
}
```

- [ ] **Step 2: Run new repository tests red**

```bash
export MCPASTE_TEST_DATABASE_URL='postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable'
go test ./internal/identity/postgres -run 'TestPairing|TestRename'
```

Expected: FAIL because pairing and device methods are undefined.

- [ ] **Step 3: Expand the store contract and transaction adapter with complete Task 8 content**

Replace `internal/identity/store.go` completely with:

```go
package identity

import (
	"context"
	"time"

	"github.com/1yoouoo/mcpaste/internal/secure"
)

type Store interface {
	WithinTx(context.Context, func(TxStore) error) error
	Authenticate(context.Context, string, string, []byte, time.Time) (Principal, error)
	ConsumeRateLimit(context.Context, RateRule, []byte, time.Time) (RateDecision, error)
	Cleanup(context.Context, time.Time) (CleanupResult, error)
}

type TxStore interface {
	GetIdempotency(context.Context, string, string, []byte) (IdempotencyRecord, error)
	DeleteIdempotency(context.Context, string, string, []byte) error
	PutIdempotency(context.Context, IdempotencyRecord) error
	InsertWorkspace(context.Context, string, time.Time) error
	InsertDevice(context.Context, string, Device) (Device, error)
	InsertCredential(context.Context, string, CredentialRecord) error
	GetRecovery(context.Context, string, string) (RecoveryRecord, error)
	PutRecovery(context.Context, string, RecoveryRecord) error
	InsertPairing(context.Context, Pairing) error
	GetPairingByID(context.Context, string, string, time.Time) (Pairing, error)
	GetPairingByShortCode(context.Context, string, string, time.Time) (Pairing, error)
	LockPairingForApproval(context.Context, string, string, time.Time) (Pairing, error)
	ApprovePairing(context.Context, string, string, string, string, time.Time, time.Time, secure.Envelope, time.Time) error
	LockPairingForClaim(context.Context, string, []byte, time.Time) (Pairing, error)
	MarkPairingClaimed(context.Context, string, time.Time) error
	ListDevices(context.Context, string, string) ([]Device, error)
	RenameDevice(context.Context, string, string, string, time.Time) (Device, error)
	RevokeDevice(context.Context, string, string, time.Time) error
	InsertEvent(context.Context, string, string, string, time.Time) error
}
```

The four string arguments after `context.Context` in `ApprovePairing` are, in order, `workspaceID`, `pairingID`, `approverDeviceID`, and `joiningDeviceID`. The first string after context remains `workspaceID` for every established-workspace operation. `InsertPairing` is the only pre-workspace mutation. `LockPairingForClaim` is the global private-capability lookup and never accepts data from a QR code alone; it and `MarkPairingClaimed` remain in the caller's `WithinTx` transaction so Task 9 can decrypt the grant before changing `claimed_at`.

Replace `internal/identity/postgres/store.go` completely with:

```go
package postgres

import (
	"context"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

type txStore struct {
	tx pgx.Tx
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) WithinTx(ctx context.Context, fn func(identity.TxStore) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(&txStore{tx: tx})
	})
}
```

This is a compile-complete replacement, not a partial diff. Steps 4 through 6 create every newly required method before Task 8 runs green; no missing-method shim or panic implementation is permitted.

- [ ] **Step 4: Write pairing persistence and private claim replay**

Create `internal/identity/postgres/pairing.go` with:

```go
package postgres

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/jackc/pgx/v5"
)

func (s *txStore) InsertPairing(ctx context.Context, pairing identity.Pairing) error {
	_, err := s.tx.Exec(ctx, `
insert into pairing_requests(
    id, short_code, claim_hash, proposed_name, platform, requested_scope,
    created_at, expires_at, metadata_purge_at
) values ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9)`,
		pairing.ID, pairing.ShortCode, pairing.ClaimHash, pairing.ProposedName,
		pairing.Platform, pairing.RequestedScope, pairing.CreatedAt,
		pairing.ExpiresAt, pairing.MetadataPurgeAt,
	)
	return err
}

func (s *txStore) GetPairingByID(ctx context.Context, workspaceID, pairingID string, now time.Time) (identity.Pairing, error) {
	pairing, err := scanPairing(s.tx.QueryRow(ctx, pairingSelect+`
where id = $2::uuid and (workspace_id is null or workspace_id = $1::uuid)`, workspaceID, pairingID))
	if err != nil {
		return identity.Pairing{}, err
	}
	if !pairing.ExpiresAt.After(now) {
		return identity.Pairing{}, identity.ErrPairingExpired
	}
	return pairing, nil
}

func (s *txStore) GetPairingByShortCode(ctx context.Context, workspaceID, shortCode string, now time.Time) (identity.Pairing, error) {
	pairing, err := scanPairing(s.tx.QueryRow(ctx, pairingSelect+`
where short_code = $2 and (workspace_id is null or workspace_id = $1::uuid)`, workspaceID, shortCode))
	if err != nil {
		return identity.Pairing{}, err
	}
	if !pairing.ExpiresAt.After(now) {
		return identity.Pairing{}, identity.ErrPairingExpired
	}
	return pairing, nil
}

func (s *txStore) LockPairingForApproval(ctx context.Context, workspaceID, pairingID string, now time.Time) (identity.Pairing, error) {
	pairing, err := scanPairing(s.tx.QueryRow(ctx, pairingSelect+`
where id = $2::uuid and (workspace_id is null or workspace_id = $1::uuid)
for update`, workspaceID, pairingID))
	if err != nil {
		return identity.Pairing{}, err
	}
	if pairing.WorkspaceID != "" {
		return identity.Pairing{}, identity.ErrPairingApproved
	}
	if !pairing.ExpiresAt.After(now) {
		return identity.Pairing{}, identity.ErrPairingExpired
	}
	return pairing, nil
}

func (s *txStore) ApprovePairing(ctx context.Context, workspaceID, pairingID, approverDeviceID, joiningDeviceID string, approvedAt, claimExpiresAt time.Time, grant secure.Envelope, purgeAt time.Time) error {
	command, err := s.tx.Exec(ctx, `
update pairing_requests set
    workspace_id = $1::uuid,
    approved_by_device_id = $3::uuid,
    device_id = $4::uuid,
    approved_at = $5,
    claim_expires_at = $6,
    grant_key_id = $7,
    grant_nonce = $8,
    grant_ciphertext = $9,
    metadata_purge_at = $10
where id = $2::uuid and workspace_id is null and expires_at > $5`,
		workspaceID, pairingID, approverDeviceID, joiningDeviceID,
		approvedAt, claimExpiresAt, grant.KeyID, grant.Nonce, grant.Ciphertext, purgeAt,
	)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return identity.ErrPairingExpired
	}
	return nil
}

func (s *txStore) LockPairingForClaim(ctx context.Context, pairingID string, claimHash []byte, now time.Time) (identity.Pairing, error) {
	pairing, err := scanPairing(s.tx.QueryRow(ctx, pairingSelect+" where id = $1::uuid for update", pairingID))
	if err != nil {
		return identity.Pairing{}, err
	}
	if subtle.ConstantTimeCompare(pairing.ClaimHash, claimHash) != 1 {
		return identity.Pairing{}, identity.ErrInvalidClaim
	}
	if pairing.WorkspaceID == "" {
		if !pairing.ExpiresAt.After(now) {
			return identity.Pairing{}, identity.ErrPairingExpired
		}
		return identity.Pairing{}, identity.ErrPairingPending
	}
	if !pairing.ClaimInvalidatedAt.IsZero() {
		return identity.Pairing{}, identity.ErrPairingExpired
	}
	if !pairing.ClaimExpiresAt.After(now) {
		return identity.Pairing{}, identity.ErrPairingExpired
	}
	return pairing, nil
}

func (s *txStore) MarkPairingClaimed(ctx context.Context, pairingID string, claimedAt time.Time) error {
	command, err := s.tx.Exec(ctx, `
update pairing_requests set claimed_at = coalesce(claimed_at, $2)
where id = $1::uuid and approved_at is not null and claim_invalidated_at is null`, pairingID, claimedAt)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return identity.ErrPairingExpired
	}
	return nil
}

const pairingSelect = `
select id::text, short_code, claim_hash, proposed_name, platform, requested_scope,
       workspace_id::text, approved_by_device_id::text, device_id::text,
       created_at, expires_at, approved_at, claim_expires_at, claimed_at, claim_invalidated_at,
       grant_key_id, grant_nonce, grant_ciphertext, metadata_purge_at
from pairing_requests `

func scanPairing(row pgx.Row) (identity.Pairing, error) {
	var pairing identity.Pairing
	var workspaceID, approverID, deviceID *string
	var approvedAt, claimExpiresAt, claimedAt, claimInvalidatedAt *time.Time
	var keyID *string
	var nonce, ciphertext []byte
	err := row.Scan(
		&pairing.ID, &pairing.ShortCode, &pairing.ClaimHash,
		&pairing.ProposedName, &pairing.Platform, &pairing.RequestedScope,
		&workspaceID, &approverID, &deviceID,
		&pairing.CreatedAt, &pairing.ExpiresAt, &approvedAt, &claimExpiresAt, &claimedAt, &claimInvalidatedAt,
		&keyID, &nonce, &ciphertext, &pairing.MetadataPurgeAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Pairing{}, identity.ErrNotFound
	}
	if err != nil {
		return identity.Pairing{}, err
	}
	if workspaceID != nil {
		pairing.WorkspaceID = *workspaceID
		pairing.ApprovedByDeviceID = *approverID
		pairing.DeviceID = *deviceID
		pairing.ApprovedAt = *approvedAt
		pairing.ClaimExpiresAt = *claimExpiresAt
		pairing.Grant = secure.Envelope{KeyID: *keyID, Nonce: nonce, Ciphertext: ciphertext}
	}
	if claimedAt != nil {
		pairing.ClaimedAt = *claimedAt
	}
	if claimInvalidatedAt != nil {
		pairing.ClaimInvalidatedAt = *claimInvalidatedAt
	}
	return pairing, nil
}
```

- [ ] **Step 5: Write device list, rename, revoke, and durable identity events**

Create `internal/identity/postgres/devices.go` with:

```go
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
)

func (s *txStore) ListDevices(ctx context.Context, workspaceID, currentDeviceID string) ([]identity.Device, error) {
	rows, err := s.tx.Query(ctx, `
select id::text, display_name, platform, role, created_at, id = $2::uuid
from devices
where workspace_id = $1::uuid and revoked_at is null
order by created_at, id`, workspaceID, currentDeviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := make([]identity.Device, 0)
	for rows.Next() {
		var device identity.Device
		if err := rows.Scan(&device.ID, &device.DisplayName, &device.Platform, &device.Role, &device.CreatedAt, &device.IsCurrent); err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (s *txStore) RenameDevice(ctx context.Context, workspaceID, deviceID, requestedName string, now time.Time) (identity.Device, error) {
	if _, err := s.tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1, 0))", workspaceID); err != nil {
		return identity.Device{}, err
	}
	var device identity.Device
	err := s.tx.QueryRow(ctx, `
select id::text, display_name, platform, role, created_at
from devices
where workspace_id = $1::uuid and id = $2::uuid and revoked_at is null
for update`, workspaceID, deviceID).Scan(
		&device.ID, &device.DisplayName, &device.Platform, &device.Role, &device.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Device{}, identity.ErrNotFound
	}
	if err != nil {
		return identity.Device{}, err
	}
	for attempt := 1; attempt <= 9999; attempt++ {
		candidate := identity.DisplayNameCandidate(requestedName, attempt)
		var exists bool
		if err := s.tx.QueryRow(ctx, `
select exists(
    select 1 from devices
    where workspace_id = $1::uuid and id <> $2::uuid and lower(display_name) = lower($3)
)`, workspaceID, deviceID, candidate).Scan(&exists); err != nil {
			return identity.Device{}, err
		}
		if exists {
			continue
		}
		if _, err := s.tx.Exec(ctx, `
update devices set display_name = $3
where workspace_id = $1::uuid and id = $2::uuid and revoked_at is null`, workspaceID, deviceID, candidate); err != nil {
			return identity.Device{}, err
		}
		device.DisplayName = candidate
		return device, nil
	}
	return identity.Device{}, identity.ErrInvalid
}

func (s *txStore) RevokeDevice(ctx context.Context, workspaceID, deviceID string, now time.Time) error {
	command, err := s.tx.Exec(ctx, `
update devices set revoked_at = $3
where workspace_id = $1::uuid and id = $2::uuid and revoked_at is null`, workspaceID, deviceID, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return identity.ErrNotFound
	}
	_, err = s.tx.Exec(ctx, `
update credentials set revoked_at = $3
where workspace_id = $1::uuid and device_id = $2::uuid and revoked_at is null`, workspaceID, deviceID, now)
	return err
}

func (s *txStore) InsertEvent(ctx context.Context, workspaceID, eventType, objectID string, now time.Time) error {
	var sequence int64
	if err := s.tx.QueryRow(ctx, `
update workspaces set next_event_sequence = next_event_sequence + 1
where id = $1::uuid
returning next_event_sequence`, workspaceID).Scan(&sequence); err != nil {
		return err
	}
	_, err := s.tx.Exec(ctx, `
insert into workspace_events(workspace_id, sequence, event_type, object_id, created_at, expires_at)
values ($1::uuid, $2, $3, $4::uuid, $5, $6)`,
		workspaceID, sequence, eventType, objectID, now, now.Add(identity.EventLifetime),
	)
	return err
}
```

- [ ] **Step 6: Write bounded cleanup with unclaimed-device revocation**

Create `internal/identity/postgres/cleanup.go` with:

```go
package postgres

import (
	"context"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
)

func (s *Store) Cleanup(ctx context.Context, now time.Time) (identity.CleanupResult, error) {
	var result identity.CleanupResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
select workspace_id::text, id::text, device_id::text
from pairing_requests
where approved_at is not null
  and claimed_at is null
  and claim_invalidated_at is null
  and claim_expires_at <= $1
order by claim_expires_at, id
for update skip locked
limit 100`, now)
		if err != nil {
			return err
		}
		type expiredGrant struct {
			workspaceID string
			pairingID   string
			deviceID    string
		}
		expired := make([]expiredGrant, 0, 100)
		for rows.Next() {
			var grant expiredGrant
			if err := rows.Scan(&grant.workspaceID, &grant.pairingID, &grant.deviceID); err != nil {
				rows.Close()
				return err
			}
			expired = append(expired, grant)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		txRepository := &txStore{tx: tx}
		for _, grant := range expired {
			devices, err := tx.Exec(ctx, `
update devices set revoked_at = $3
where workspace_id = $1::uuid and id = $2::uuid and revoked_at is null`,
				grant.workspaceID, grant.deviceID, now)
			if err != nil {
				return err
			}
			result.RevokedDevices += devices.RowsAffected()
			if _, err := tx.Exec(ctx, `
update credentials set revoked_at = $3
where workspace_id = $1::uuid and device_id = $2::uuid and revoked_at is null`,
				grant.workspaceID, grant.deviceID, now); err != nil {
				return err
			}
			if err := txRepository.InsertEvent(ctx, grant.workspaceID, "device.revoked", grant.deviceID, now); err != nil {
				return err
			}
			command, err := tx.Exec(ctx, `
update pairing_requests set claim_invalidated_at = $3
where workspace_id = $1::uuid and id = $2::uuid
  and claimed_at is null and claim_invalidated_at is null`,
				grant.workspaceID, grant.pairingID, now)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return identity.ErrPairingExpired
			}
		}

		pairings, err := tx.Exec(ctx, `
delete from pairing_requests
where metadata_purge_at <= $1
  and (approved_at is null or claimed_at is not null or claim_invalidated_at is not null)`, now)
		if err != nil {
			return err
		}
		result.PairingRows = pairings.RowsAffected()
		idempotency, err := tx.Exec(ctx, "delete from idempotency_records where expires_at <= clock_timestamp()")
		if err != nil {
			return err
		}
		result.IdempotencyRows = idempotency.RowsAffected()
		events, err := tx.Exec(ctx, "delete from workspace_events where expires_at <= $1", now)
		if err != nil {
			return err
		}
		result.EventRows = events.RowsAffected()
		rateLimits, err := tx.Exec(ctx, "delete from rate_limit_buckets where expires_at <= $1", now)
		if err != nil {
			return err
		}
		result.RateLimitRows = rateLimits.RowsAffected()
		return nil
	})
	return result, err
}
```

- [ ] **Step 7: Run all repository tests and inspect workspace predicates**

```bash
gofmt -w internal/identity/postgres
export MCPASTE_TEST_DATABASE_URL='postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable'
go test -race ./internal/identity/postgres
go vet ./internal/identity/postgres
rg -n 'where .*id = \$[0-9]' internal/identity/postgres
```

Expected: tests pass. Every established device, credential, recovery, event, and approved pairing SQL predicate shown by the search includes `workspace_id`; the only global IDs are pending pairing insertion and private claim capability lookup described in `store.go`.

- [ ] **Step 8: Add cleanup integration assertions**

Append these tests to `internal/identity/postgres/store_integration_test.go`:

```go
func TestCleanupPurgesExpiredMetadata(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	_, err := store.ConsumeRateLimit(ctx, identity.RateRule{Scope: "cleanup", Limit: 1, Window: time.Minute}, bytes.Repeat([]byte{0x91}, 32), now.Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("seed rate limit: %v", err)
	}
	result, err := store.Cleanup(ctx, now)
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RateLimitRows != 1 {
		t.Fatalf("RateLimitRows = %d", result.RateLimitRows)
	}
}

func TestClaimAndCleanupSerializeGrantValidity(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	claimAt := createdAt.Add(4*time.Minute + 59*time.Second)
	cleanupAt := createdAt.Add(6 * time.Minute)
	pairingID := "00000000-0000-4000-8000-000000000311"
	joiningDeviceID := "00000000-0000-4000-8000-000000000312"
	claimHash := bytes.Repeat([]byte{0xa1}, 32)
	credentialHash := bytes.Repeat([]byte{0xa2}, 32)
	credentialLocator := "CCCCCCCCCCCCCCCCCCCCCC"
	grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0xa3}, 12), Ciphertext: []byte{0xa4}}
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, createdAt); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "2345678A", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: createdAt, ExpiresAt: createdAt.Add(identity.PairingLifetime),
			MetadataPurgeAt: createdAt.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: joiningDeviceID, DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{
			DeviceID: joining.ID, Locator: credentialLocator, Scope: "connector", Hash: credentialHash, CreatedAt: createdAt,
		}); err != nil {
			return err
		}
		return tx.ApprovePairing(
			ctx, workspaceOne, pairingID, approver.ID, joining.ID,
			createdAt, createdAt.Add(identity.ClaimLifetime), grant,
			createdAt.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime),
		)
	})
	if err != nil {
		t.Fatalf("seed claim/cleanup race: %v", err)
	}

	type cleanupOutcome struct {
		result identity.CleanupResult
		err    error
	}
	start := make(chan struct{})
	claimDone := make(chan error, 1)
	cleanupDone := make(chan cleanupOutcome, 1)
	go func() {
		<-start
		err := store.WithinTx(ctx, func(tx identity.TxStore) error {
			if _, err := tx.LockPairingForClaim(ctx, pairingID, claimHash, claimAt); err != nil {
				return err
			}
			return tx.MarkPairingClaimed(ctx, pairingID, claimAt)
		})
		claimDone <- err
	}()
	go func() {
		<-start
		result, err := store.Cleanup(ctx, cleanupAt)
		cleanupDone <- cleanupOutcome{result: result, err: err}
	}()
	close(start)
	claimErr := <-claimDone
	cleanup := <-cleanupDone
	if cleanup.err != nil {
		t.Fatalf("Cleanup() error = %v", cleanup.err)
	}

	var claimedAt, invalidatedAt *time.Time
	if err := pool.QueryRow(ctx, `
select claimed_at, claim_invalidated_at
from pairing_requests
where workspace_id = $1::uuid and id = $2::uuid`, workspaceOne, pairingID).Scan(&claimedAt, &invalidatedAt); err != nil {
		t.Fatalf("inspect pairing terminal state: %v", err)
	}
	switch {
	case claimErr == nil:
		if cleanup.result.RevokedDevices != 0 || claimedAt == nil || invalidatedAt != nil {
			t.Fatalf("claim-won state: revoked=%d claimed=%v invalidated=%v", cleanup.result.RevokedDevices, claimedAt != nil, invalidatedAt != nil)
		}
		if _, err := store.Authenticate(ctx, workspaceOne, credentialLocator, credentialHash, cleanupAt); err != nil {
			t.Fatalf("claim-won credential authentication: %v", err)
		}
	case errors.Is(claimErr, identity.ErrPairingExpired):
		if cleanup.result.RevokedDevices != 1 || claimedAt != nil || invalidatedAt == nil {
			t.Fatalf("cleanup-won state: revoked=%d claimed=%v invalidated=%v", cleanup.result.RevokedDevices, claimedAt != nil, invalidatedAt != nil)
		}
		if _, err := store.Authenticate(ctx, workspaceOne, credentialLocator, credentialHash, cleanupAt); !errors.Is(err, identity.ErrUnauthorized) {
			t.Fatalf("cleanup-won authentication error = %v", err)
		}
		var eventCount int
		if err := pool.QueryRow(ctx, `
select count(*)
from workspace_events
where workspace_id = $1::uuid and event_type = 'device.revoked' and object_id = $2::uuid`,
			workspaceOne, joiningDeviceID).Scan(&eventCount); err != nil {
			t.Fatalf("count cleanup event: %v", err)
		}
		if eventCount != 1 {
			t.Fatalf("cleanup device.revoked events = %d", eventCount)
		}
	default:
		t.Fatalf("claim error = %v", claimErr)
	}
}

func TestCleanupWinsDeterministicallyAndRevokesGrant(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	cleanupAt := createdAt.Add(6 * time.Minute)
	pairingID := "00000000-0000-4000-8000-000000000321"
	joiningDeviceID := "00000000-0000-4000-8000-000000000322"
	claimHash := bytes.Repeat([]byte{0xb1}, 32)
	credentialHash := bytes.Repeat([]byte{0xb2}, 32)
	credentialLocator := "DDDDDDDDDDDDDDDDDDDDDD"
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, createdAt); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "2345678B", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: createdAt, ExpiresAt: createdAt.Add(identity.PairingLifetime),
			MetadataPurgeAt: createdAt.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: joiningDeviceID, DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{
			DeviceID: joining.ID, Locator: credentialLocator, Scope: "connector", Hash: credentialHash, CreatedAt: createdAt,
		}); err != nil {
			return err
		}
		grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0xb3}, 12), Ciphertext: []byte{0xb4}}
		return tx.ApprovePairing(
			ctx, workspaceOne, pairingID, approver.ID, joining.ID,
			createdAt, createdAt.Add(identity.ClaimLifetime), grant,
			createdAt.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime),
		)
	})
	if err != nil {
		t.Fatalf("seed deterministic cleanup: %v", err)
	}
	result, err := store.Cleanup(ctx, cleanupAt)
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RevokedDevices != 1 {
		t.Fatalf("RevokedDevices = %d", result.RevokedDevices)
	}
	if _, err := store.Authenticate(ctx, workspaceOne, credentialLocator, credentialHash, cleanupAt); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("revoked credential authentication error = %v", err)
	}
	var deviceRevoked, credentialRevoked, invalidatedAt *time.Time
	var eventCount int
	if err := pool.QueryRow(ctx, `
select d.revoked_at, c.revoked_at, p.claim_invalidated_at,
       (select count(*) from workspace_events e
        where e.workspace_id = p.workspace_id and e.event_type = 'device.revoked' and e.object_id = p.device_id)
from pairing_requests p
join devices d on d.workspace_id = p.workspace_id and d.id = p.device_id
join credentials c on c.workspace_id = d.workspace_id and c.device_id = d.id
where p.workspace_id = $1::uuid and p.id = $2::uuid`, workspaceOne, pairingID).Scan(
		&deviceRevoked, &credentialRevoked, &invalidatedAt, &eventCount,
	); err != nil {
		t.Fatalf("inspect cleanup state: %v", err)
	}
	if deviceRevoked == nil || credentialRevoked == nil || invalidatedAt == nil || eventCount != 1 {
		t.Fatalf("cleanup state metadata: device=%v credential=%v invalidated=%v events=%d", deviceRevoked != nil, credentialRevoked != nil, invalidatedAt != nil, eventCount)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		_, err := tx.LockPairingForClaim(ctx, pairingID, claimHash, cleanupAt)
		return err
	})
	if !errors.Is(err, identity.ErrPairingExpired) {
		t.Fatalf("claim after cleanup error = %v", err)
	}
}
```

Run:

```bash
gofmt -w internal/identity/postgres/store_integration_test.go
go test -race ./internal/identity/postgres -run 'TestCleanup|TestClaimAndCleanup'
```

Expected: PASS. The deterministic test proves cleanup revokes the joining device and credential, writes exactly one durable `device.revoked` event, invalidates the pairing, and makes later claim locking fail. The separate race test proves that either claim commits first and cleanup skips the locked/claimed row while the credential remains valid, or cleanup commits first so claim cannot return the encrypted grant.

- [ ] **Step 9: Commit the completed persistence boundary**

```bash
git add internal/identity/store.go internal/identity/postgres
git commit -m "feat: persist pairing and device lifecycle"
```

Expected: one compile-safe commit containing the expanded store interface, pairing claim replay, device administration, durable identity events, cleanup, and integration tests.

## Task 9: Orchestrate identity transactions and encrypted replay

**Files:**

- Modify: `internal/secure/credential.go`
- Modify: `internal/identity/model.go`
- Modify: `internal/identity/store.go`
- Modify: `internal/identity/postgres/idempotency.go`
- Create: `internal/identity/service_test.go`
- Create: `internal/identity/service.go`

- [ ] **Step 1: Export UUID validation and add the result type**

In `internal/secure/credential.go`, rename `validUUID` to `ValidUUID` and update both callers in that file and `internal/secure/recovery.go` to call `ValidUUID`.

Append this type to `internal/identity/model.go`:

```go
type Result struct {
	Status int
	Body   []byte
}
```

In `internal/identity/store.go`, add this method as the first `TxStore` method:

```go
	LockIdempotency(context.Context, string, string, []byte) error
```

Add this complete method to `internal/identity/postgres/idempotency.go`:

```go
func (s *txStore) LockIdempotency(ctx context.Context, scopeID, operation string, keyHash []byte) error {
	_, err := s.tx.Exec(ctx, `
select pg_advisory_xact_lock(hashtextextended($1 || ':' || $2 || ':' || encode($3, 'hex'), 0))`, scopeID, operation, keyHash)
	return err
}
```

- [ ] **Step 2: Write deterministic workspace and idempotency service tests**

Create `internal/identity/service_test.go` with:

```go
package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	identitypostgres "github.com/1yoouoo/mcpaste/internal/identity/postgres"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/1yoouoo/mcpaste/internal/testdb"
)

type fixedClock struct{ value time.Time }

func (c fixedClock) Now() time.Time { return c.value }

type countingReader struct{ next byte }

func (r *countingReader) Read(target []byte) (int, error) {
	for index := range target {
		target[index] = r.next
		r.next++
	}
	return len(target), nil
}

type shortCodeGuardStore struct {
	identity.Store
	tx            *shortCodeGuardTx
	rateCalls     int
	withinTxCalls int
}

type shortCodeGuardTx struct {
	identity.TxStore
	lookupCalls int
}

var errMutationTransactionReached = errors.New("mutation transaction reached")

type recoveryPrecomputeStore struct {
	identity.Store
	tx            *recoveryPrecomputeTx
	withinTxCalls atomic.Int32
}

type recoveryPrecomputeTx struct {
	identity.TxStore
}

type recoveryPermitGuardStore struct {
	identity.Store
	rateCalls     atomic.Int32
	withinTxCalls atomic.Int32
}

type observedDoneContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func (s *recoveryPrecomputeStore) ConsumeRateLimit(context.Context, identity.RateRule, []byte, time.Time) (identity.RateDecision, error) {
	return identity.RateDecision{Allowed: true}, nil
}

func (s *recoveryPrecomputeStore) WithinTx(ctx context.Context, fn func(identity.TxStore) error) error {
	if s.withinTxCalls.Add(1) == 1 {
		return fn(s.tx)
	}
	return errMutationTransactionReached
}

func (s *recoveryPrecomputeTx) GetIdempotency(context.Context, string, string, []byte) (identity.IdempotencyRecord, error) {
	return identity.IdempotencyRecord{}, identity.ErrNotFound
}

func (s *recoveryPermitGuardStore) ConsumeRateLimit(context.Context, identity.RateRule, []byte, time.Time) (identity.RateDecision, error) {
	s.rateCalls.Add(1)
	panic("recovery reached rate limiting without a permit")
}

func (s *recoveryPermitGuardStore) WithinTx(context.Context, func(identity.TxStore) error) error {
	s.withinTxCalls.Add(1)
	panic("recovery reached a transaction without a permit")
}

type blockingRecoveryReader struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (r *blockingRecoveryReader) Read(target []byte) (int, error) {
	for index := range target {
		target[index] = byte(index + 1)
	}
	if r.calls.Add(1) == 3 {
		close(r.started)
		<-r.release
	}
	return len(target), nil
}

func (s *shortCodeGuardStore) ConsumeRateLimit(context.Context, identity.RateRule, []byte, time.Time) (identity.RateDecision, error) {
	s.rateCalls++
	panic("malformed short code reached rate limiting")
}

func (s *shortCodeGuardStore) WithinTx(ctx context.Context, fn func(identity.TxStore) error) error {
	s.withinTxCalls++
	return fn(s.tx)
}

func (s *shortCodeGuardTx) GetPairingByShortCode(context.Context, string, string, time.Time) (identity.Pairing, error) {
	s.lookupCalls++
	panic("malformed short code reached repository lookup")
}

func TestCreateWorkspaceReturnsExactlyTwoCredentialsAndReplaysBytes(t *testing.T) {
	pool := testdb.New(t)
	random := &countingReader{next: 1}
	keyring, err := secure.NewKeyring("test-key", map[string][]byte{"test-key": bytes.Repeat([]byte{0x33}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	clock := fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	service := identity.NewService(identitypostgres.New(pool), keyring, random, clock)
	idempotencyKey := "00000000-0000-4000-8000-000000000901"
	first, err := service.CreateWorkspace(context.Background(), "192.0.2.10", idempotencyKey, identity.CreateWorkspaceInput{
		DeviceName: "MacBook Pro", Platform: "macos",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	second, err := service.CreateWorkspace(context.Background(), "192.0.2.10", idempotencyKey, identity.CreateWorkspaceInput{
		DeviceName: "MacBook Pro", Platform: "macos",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace() replay error = %v", err)
	}
	if first.Status != 201 || !bytes.Equal(first.Body, second.Body) {
		t.Fatalf("status/replay differ")
	}
	var grant identity.WorkspaceGrant
	if err := json.Unmarshal(first.Body, &grant); err != nil {
		t.Fatalf("unmarshal grant: %v", err)
	}
	if len(grant.Credentials) != 2 {
		t.Fatalf("credential count = %d", len(grant.Credentials))
	}
	if grant.Credentials[0].Kind != "full" || grant.Credentials[1].Kind != "connector" {
		t.Fatal("credential kinds are incorrect")
	}
	if grant.Credentials[0].Token == "" || grant.Credentials[1].Token == "" {
		t.Fatal("one or more issued credentials were empty")
	}
	if grant.RecoveryCode == "" || grant.Device.Role != "full" {
		t.Fatalf("workspace grant metadata is incomplete")
	}
	var storedSecrets int
	if err := pool.QueryRow(context.Background(), `
select count(*) from credentials
where workspace_id = $1::uuid and octet_length(secret_hash) = 32`, grant.WorkspaceID).Scan(&storedSecrets); err != nil {
		t.Fatalf("count credentials: %v", err)
	}
	if storedSecrets != 2 {
		t.Fatalf("stored credential count = %d", storedSecrets)
	}
}

func TestCreateWorkspaceBuildsRecoveryBeforeMutationTransaction(t *testing.T) {
	store := &recoveryPrecomputeStore{tx: &recoveryPrecomputeTx{}}
	random := &blockingRecoveryReader{started: make(chan struct{}), release: make(chan struct{})}
	service := identity.NewService(
		store,
		nil,
		random,
		fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)},
	)
	result := make(chan error, 1)
	go func() {
		_, err := service.CreateWorkspace(context.Background(), "192.0.2.13", "00000000-0000-4000-8000-000000000904", identity.CreateWorkspaceInput{
			DeviceName: "Precomputed Recovery",
			Platform:   "macos",
		})
		result <- err
	}()
	<-random.started
	if calls := store.withinTxCalls.Load(); calls != 1 {
		t.Fatalf("transactions before recovery generation = %d", calls)
	}
	close(random.release)
	if err := <-result; !errors.Is(err, errMutationTransactionReached) {
		t.Fatal("workspace creation did not reach the mutation transaction after recovery generation")
	}
	if calls := store.withinTxCalls.Load(); calls != 2 {
		t.Fatalf("transactions after recovery generation = %d", calls)
	}
}

func TestThirdRecoveryCancelsBeforeDatabaseWhileTwoPermitsAreHeld(t *testing.T) {
	first, err := secure.AcquireRecoveryPermit(context.Background())
	if err != nil {
		t.Fatal("acquire first recovery permit failed")
	}
	defer first.Release()
	second, err := secure.AcquireRecoveryPermit(context.Background())
	if err != nil {
		t.Fatal("acquire second recovery permit failed")
	}
	defer second.Release()

	store := &recoveryPermitGuardStore{}
	service := identity.NewService(store, nil, nil, fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)})
	base, cancel := context.WithCancel(context.Background())
	ctx := &observedDoneContext{Context: base, observed: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, recoverErr := service.Recover(ctx, "192.0.2.14", "00000000-0000-4000-8000-000000000905", identity.RecoveryInput{
			RecoveryCode: "mcr1.00000000-0000-4000-8000-000000000001." + strings.Repeat("A", 22) + "." + strings.Repeat("A", 43),
			DeviceName:   "Permit Guard",
			Platform:     "macos",
		})
		result <- recoverErr
	}()
	select {
	case <-ctx.observed:
	case <-time.After(time.Second):
		t.Fatal("third recovery did not wait for a permit")
	}
	cancel()
	select {
	case recoverErr := <-result:
		if !errors.Is(recoverErr, context.Canceled) {
			t.Fatal("third recovery did not return context cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("third recovery did not stop after cancellation")
	}
	if store.rateCalls.Load() != 0 || store.withinTxCalls.Load() != 0 {
		t.Fatalf("rate/transaction calls = %d/%d", store.rateCalls.Load(), store.withinTxCalls.Load())
	}
}

func TestCreateWorkspaceRejectsChangedIdempotentRequest(t *testing.T) {
	pool := testdb.New(t)
	random := &countingReader{next: 1}
	keyring, err := secure.NewKeyring("test-key", map[string][]byte{"test-key": bytes.Repeat([]byte{0x44}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	service := identity.NewService(identitypostgres.New(pool), keyring, random, fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)})
	key := "00000000-0000-4000-8000-000000000902"
	if _, err := service.CreateWorkspace(context.Background(), "192.0.2.11", key, identity.CreateWorkspaceInput{DeviceName: "First", Platform: "macos"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := service.CreateWorkspace(context.Background(), "192.0.2.11", key, identity.CreateWorkspaceInput{DeviceName: "Second", Platform: "macos"}); err != identity.ErrIdempotencyConflict {
		t.Fatalf("changed request error = %v", err)
	}
}

func TestCreateWorkspaceReusesExpiredIdempotencyKey(t *testing.T) {
	pool := testdb.New(t)
	random := &countingReader{next: 1}
	keyring, err := secure.NewKeyring("test-key", map[string][]byte{"test-key": bytes.Repeat([]byte{0x45}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	clock := &fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	service := identity.NewService(identitypostgres.New(pool), keyring, random, clock)
	key := "00000000-0000-4000-8000-000000000903"
	input := identity.CreateWorkspaceInput{DeviceName: "Reusable", Platform: "macos"}
	first, err := service.CreateWorkspace(context.Background(), "192.0.2.12", key, input)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
update idempotency_records
set created_at = stamp.now - interval '25 hours',
    expires_at = stamp.now - interval '1 hour'
from (select clock_timestamp() as now) stamp
where scope_id = 'public' and operation = 'workspace.create'`); err != nil {
		t.Fatalf("expire idempotency row: %v", err)
	}
	second, err := service.CreateWorkspace(context.Background(), "192.0.2.12", key, input)
	if err != nil {
		t.Fatalf("expired-key create: %v", err)
	}
	if first.Status != 201 || second.Status != 201 || bytes.Equal(first.Body, second.Body) {
		t.Fatal("expired idempotency key replayed the old workspace")
	}
	var workspaces, idempotencyRows int
	if err := pool.QueryRow(context.Background(), "select count(*) from workspaces").Scan(&workspaces); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `
select count(*) from idempotency_records where scope_id = 'public' and operation = 'workspace.create'`).Scan(&idempotencyRows); err != nil {
		t.Fatalf("count idempotency rows: %v", err)
	}
	if workspaces != 2 || idempotencyRows != 1 {
		t.Fatalf("workspace/idempotency rows = %d/%d", workspaces, idempotencyRows)
	}
}

func TestPairingByShortCodeRejectsMalformedBeforeRateAndTransaction(t *testing.T) {
	store := &shortCodeGuardStore{tx: &shortCodeGuardTx{}}
	service := identity.NewService(
		store, nil, nil,
		fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)},
	)
	principal := identity.Principal{
		WorkspaceID: "00000000-0000-4000-8000-000000000101",
		DeviceID:    "00000000-0000-4000-8000-000000000201",
		Scope:       "full",
	}
	if _, err := service.PairingByShortCode(context.Background(), principal, "I2345678"); !errors.Is(err, identity.ErrInvalid) {
		t.Fatalf("PairingByShortCode() error = %v", err)
	}
	if store.rateCalls != 0 || store.withinTxCalls != 0 || store.tx.lookupCalls != 0 {
		t.Fatalf("rate/transaction/lookup calls = %d/%d/%d", store.rateCalls, store.withinTxCalls, store.tx.lookupCalls)
	}
}

func TestPairingByShortCodeMalformedLeavesNoRateLimitRowIntegration(t *testing.T) {
	pool := testdb.New(t)
	random := &countingReader{next: 1}
	keyring, err := secure.NewKeyring("test-key", map[string][]byte{"test-key": bytes.Repeat([]byte{0x46}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	service := identity.NewService(
		identitypostgres.New(pool), keyring, random,
		fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)},
	)
	principal := identity.Principal{
		WorkspaceID: "00000000-0000-4000-8000-000000000101",
		DeviceID:    "00000000-0000-4000-8000-000000000201",
		Scope:       "full",
	}
	if _, err := service.PairingByShortCode(context.Background(), principal, "I2345678"); !errors.Is(err, identity.ErrInvalid) {
		t.Fatalf("PairingByShortCode() error = %v", err)
	}
	var rateRows int
	if err := pool.QueryRow(context.Background(), `
select count(*) from rate_limit_buckets where scope = 'pairing.lookup'`).Scan(&rateRows); err != nil {
		t.Fatalf("count rate-limit rows: %v", err)
	}
	if rateRows != 0 {
		t.Fatalf("malformed short code consumed rate limit: rows = %d", rateRows)
	}
}

func TestClaimDecryptFailureRollsBackAndRemainsCleanupEligible(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	random := &countingReader{next: 1}
	keyring, err := secure.NewKeyring("test-key", map[string][]byte{"test-key": bytes.Repeat([]byte{0x47}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	store := identitypostgres.New(pool)
	service := identity.NewService(store, keyring, random, fixedClock{value: now})
	workspaceID := "00000000-0000-4000-8000-000000000111"
	approverID := "00000000-0000-4000-8000-000000000211"
	joiningID := "00000000-0000-4000-8000-000000000212"
	pairingID := "00000000-0000-4000-8000-000000000311"
	claimSecret, claimHash, err := secure.NewClaimSecret(bytes.NewReader(bytes.Repeat([]byte{0x48}, 32)))
	if err != nil {
		t.Fatalf("NewClaimSecret() error = %v", err)
	}
	credentialHash := bytes.Repeat([]byte{0x49}, 32)
	credentialLocator := "EEEEEEEEEEEEEEEEEEEEEE"
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, now); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceID, identity.Device{ID: approverID, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "2345678C", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: now, ExpiresAt: now.Add(identity.PairingLifetime),
			MetadataPurgeAt: now.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceID, identity.Device{ID: joiningID, DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.InsertCredential(ctx, workspaceID, identity.CredentialRecord{
			DeviceID: joining.ID, Locator: credentialLocator, Scope: "connector", Hash: credentialHash, CreatedAt: now,
		}); err != nil {
			return err
		}
		grant := secure.Envelope{KeyID: "missing-key", Nonce: bytes.Repeat([]byte{0x4a}, 12), Ciphertext: []byte{0x4b}}
		return tx.ApprovePairing(ctx, workspaceID, pairingID, approver.ID, joining.ID, now, now.Add(identity.ClaimLifetime), grant, now.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime))
	})
	if err != nil {
		t.Fatalf("seed corrupt claim grant: %v", err)
	}
	if _, err := service.ClaimPairing(ctx, "192.0.2.19", pairingID, claimSecret); err == nil {
		t.Fatal("ClaimPairing() error = nil for missing encryption key")
	}
	var claimedAt *time.Time
	if err := pool.QueryRow(ctx, "select claimed_at from pairing_requests where id = $1::uuid", pairingID).Scan(&claimedAt); err != nil {
		t.Fatalf("inspect claimed_at: %v", err)
	}
	if claimedAt != nil {
		t.Fatal("decrypt failure committed claimed_at")
	}
	result, err := store.Cleanup(ctx, now.Add(6*time.Minute))
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RevokedDevices != 1 {
		t.Fatalf("RevokedDevices = %d", result.RevokedDevices)
	}
	if _, err := store.Authenticate(ctx, workspaceID, credentialLocator, credentialHash, now.Add(6*time.Minute)); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("cleanup authentication error = %v", err)
	}
	var eventCount int
	if err := pool.QueryRow(ctx, `
select count(*) from workspace_events
where workspace_id = $1::uuid and event_type = 'device.revoked' and object_id = $2::uuid`, workspaceID, joiningID).Scan(&eventCount); err != nil {
		t.Fatalf("count cleanup event: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("cleanup event count = %d", eventCount)
	}
}

```

- [ ] **Step 3: Run service tests red**

```bash
export MCPASTE_TEST_DATABASE_URL='postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable'
go test ./internal/identity -run 'TestCreateWorkspace|TestPairingByShortCode|TestThirdRecoveryCancelsBeforeDatabaseWhileTwoPermitsAreHeld'
```

Expected: FAIL because `NewService`, `CreateWorkspaceInput`, `CreateWorkspace`, `PairingByShortCode`, and `Recover` are undefined. The permit guard must not record a rate-limit call or transaction before cancellation.

- [ ] **Step 4: Write the complete identity service**

Create `internal/identity/service.go` with:

```go
package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/1yoouoo/mcpaste/internal/secure"
)

type Service struct {
	store   Store
	keyring *secure.Keyring
	random  secure.Random
	clock   Clock
}

type CreateWorkspaceInput struct {
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
}

type CreatePairingInput struct {
	ProposedName   string `json:"proposed_name"`
	Platform       string `json:"platform"`
	RequestedScope string `json:"requested_scope"`
}

type RecoveryInput struct {
	RecoveryCode string `json:"recovery_code"`
	DeviceName   string `json:"device_name"`
	Platform     string `json:"platform"`
}

type RenameInput struct {
	DisplayName string `json:"display_name"`
}

const publicIdempotencyScope = "public"

func NewService(store Store, keyring *secure.Keyring, random secure.Random, clock Clock) *Service {
	return &Service{store: store, keyring: keyring, random: random, clock: clock}
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	parsed, err := secure.ParseCredential(token)
	if err != nil {
		return Principal{}, ErrUnauthorized
	}
	return s.store.Authenticate(ctx, parsed.WorkspaceID, parsed.Locator, parsed.Hash, s.clock.Now())
}

func (s *Service) CreateWorkspace(ctx context.Context, clientIP, idempotencyKey string, input CreateWorkspaceInput) (Result, error) {
	name, err := NormalizeDisplayName(input.DeviceName)
	if err != nil || input.Platform != "macos" {
		return Result{}, ErrInvalid
	}
	input.DeviceName = name
	canonical, _ := json.Marshal(input)
	if replay, found, err := s.preflight(ctx, publicIdempotencyScope, "workspace.create", idempotencyKey, "", canonical); err != nil || found {
		return replay, err
	}
	if err := s.limit(ctx, RateRule{Scope: "workspace.create", Limit: 5, Window: time.Hour}, "ip:"+clientIP); err != nil {
		return Result{}, err
	}
	workspaceID, err := secure.NewUUID(s.random)
	if err != nil {
		return Result{}, err
	}
	deviceID, err := secure.NewUUID(s.random)
	if err != nil {
		return Result{}, err
	}
	recovery, err := secure.NewRecovery(ctx, workspaceID, s.random)
	if err != nil {
		return Result{}, err
	}
	return s.mutate(ctx, publicIdempotencyScope, "workspace.create", idempotencyKey, "", canonical, 201, func(tx TxStore, now time.Time) (any, string, error) {
		if err := tx.InsertWorkspace(ctx, workspaceID, now); err != nil {
			return nil, "", err
		}
		device, err := tx.InsertDevice(ctx, workspaceID, Device{ID: deviceID, DisplayName: name, Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil {
			return nil, "", err
		}
		issued, records, err := s.issueCredentials(workspaceID, deviceID, "full", now)
		if err != nil {
			return nil, "", err
		}
		for _, record := range records {
			if err := tx.InsertCredential(ctx, workspaceID, record); err != nil {
				return nil, "", err
			}
		}
		if err := tx.PutRecovery(ctx, workspaceID, RecoveryRecord{
			WorkspaceID: workspaceID, Locator: recovery.Locator, Verifier: recovery.Verifier,
			CreatedAt: now, RotatedAt: now,
		}); err != nil {
			return nil, "", err
		}
		if err := tx.InsertEvent(ctx, workspaceID, "device.added", deviceID, now); err != nil {
			return nil, "", err
		}
		return WorkspaceGrant{WorkspaceID: workspaceID, Device: grantDevice(device), Credentials: issued, RecoveryCode: recovery.Code}, workspaceID, nil
	})
}

func (s *Service) CreatePairing(ctx context.Context, clientIP, idempotencyKey string, input CreatePairingInput) (Result, error) {
	name, err := NormalizeDisplayName(input.ProposedName)
	if err != nil || (input.Platform != "macos" && input.Platform != "linux") || (input.RequestedScope != "full" && input.RequestedScope != "connector") || input.RequestedScope == "full" && input.Platform != "macos" {
		return Result{}, ErrInvalid
	}
	input.ProposedName = name
	canonical, _ := json.Marshal(input)
	if replay, found, err := s.preflight(ctx, publicIdempotencyScope, "pairing.create", idempotencyKey, "", canonical); err != nil || found {
		return replay, err
	}
	if err := s.limit(ctx, RateRule{Scope: "pairing.create", Limit: 10, Window: 10 * time.Minute}, "ip:"+clientIP); err != nil {
		return Result{}, err
	}
	return s.mutate(ctx, publicIdempotencyScope, "pairing.create", idempotencyKey, "", canonical, 201, func(tx TxStore, now time.Time) (any, string, error) {
		for attempt := 0; attempt < 8; attempt++ {
			pairingID, err := secure.NewUUID(s.random)
			if err != nil {
				return nil, "", err
			}
			shortCode, err := s.newShortCode()
			if err != nil {
				return nil, "", err
			}
			claimSecret, claimHash, err := secure.NewClaimSecret(s.random)
			if err != nil {
				return nil, "", err
			}
			expiresAt := now.Add(PairingLifetime)
			err = tx.InsertPairing(ctx, Pairing{
				ID: pairingID, ShortCode: shortCode, ClaimHash: claimHash,
				ProposedName: name, Platform: input.Platform, RequestedScope: input.RequestedScope,
				CreatedAt: now, ExpiresAt: expiresAt, MetadataPurgeAt: expiresAt.Add(PairingMetadataLifetime),
			})
			if errors.Is(err, ErrInvalid) {
				continue
			}
			if err != nil {
				return nil, "", err
			}
			return PairingCreateResponse{
				PairingID: pairingID, QRPayload: "mcpaste://pair/" + pairingID,
				ShortCode: shortCode, ClaimSecret: claimSecret, ExpiresAt: wireTime(expiresAt),
			}, "", nil
		}
		return nil, "", errors.New("pairing identifier collision limit reached")
	})
}

func (s *Service) PairingByID(ctx context.Context, principal Principal, pairingID string) (PairingDetails, error) {
	if principal.Scope != "full" {
		return PairingDetails{}, ErrForbidden
	}
	if !secure.ValidUUID(pairingID) {
		return PairingDetails{}, ErrInvalid
	}
	var pairing Pairing
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		pairing, err = tx.GetPairingByID(ctx, principal.WorkspaceID, pairingID, s.clock.Now())
		return err
	})
	return details(pairing), err
}

func (s *Service) PairingByShortCode(ctx context.Context, principal Principal, shortCode string) (PairingDetails, error) {
	if principal.Scope != "full" {
		return PairingDetails{}, ErrForbidden
	}
	if !validShortCode(shortCode) {
		return PairingDetails{}, ErrInvalid
	}
	if err := s.limit(ctx, RateRule{Scope: "pairing.lookup", Limit: 30, Window: 5 * time.Minute}, principal.WorkspaceID+":"+principal.DeviceID); err != nil {
		return PairingDetails{}, err
	}
	var pairing Pairing
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		pairing, err = tx.GetPairingByShortCode(ctx, principal.WorkspaceID, shortCode, s.clock.Now())
		return err
	})
	return details(pairing), err
}

func (s *Service) ApprovePairing(ctx context.Context, principal Principal, pairingID, idempotencyKey string) (Result, error) {
	if principal.Scope != "full" {
		return Result{}, ErrForbidden
	}
	if !secure.ValidUUID(pairingID) {
		return Result{}, ErrInvalid
	}
	canonical := []byte("{}")
	if replay, found, err := s.preflight(ctx, principal.WorkspaceID, "pairing.approve:"+pairingID, idempotencyKey, principal.WorkspaceID, canonical); err != nil || found {
		return replay, err
	}
	return s.mutate(ctx, principal.WorkspaceID, "pairing.approve:"+pairingID, idempotencyKey, principal.WorkspaceID, canonical, 200, func(tx TxStore, now time.Time) (any, string, error) {
		pairing, err := tx.LockPairingForApproval(ctx, principal.WorkspaceID, pairingID, now)
		if err != nil {
			return nil, "", err
		}
		deviceID, err := secure.NewUUID(s.random)
		if err != nil {
			return nil, "", err
		}
		role := pairing.RequestedScope
		device, err := tx.InsertDevice(ctx, principal.WorkspaceID, Device{
			ID: deviceID, DisplayName: pairing.ProposedName, Platform: pairing.Platform, Role: role, CreatedAt: now,
		})
		if err != nil {
			return nil, "", err
		}
		issued, records, err := s.issueCredentials(principal.WorkspaceID, deviceID, role, now)
		if err != nil {
			return nil, "", err
		}
		for _, record := range records {
			if err := tx.InsertCredential(ctx, principal.WorkspaceID, record); err != nil {
				return nil, "", err
			}
		}
		grantBody, err := marshalLine(WorkspaceGrant{WorkspaceID: principal.WorkspaceID, Device: grantDevice(device), Credentials: issued})
		if err != nil {
			return nil, "", err
		}
		grant, err := s.keyring.Encrypt("pairing-grant", pairingID, grantBody)
		if err != nil {
			return nil, "", err
		}
		claimExpiresAt := now.Add(ClaimLifetime)
		if err := tx.ApprovePairing(ctx, principal.WorkspaceID, pairingID, principal.DeviceID, deviceID, now, claimExpiresAt, grant, claimExpiresAt.Add(PairingMetadataLifetime)); err != nil {
			return nil, "", err
		}
		if err := tx.InsertEvent(ctx, principal.WorkspaceID, "device.added", deviceID, now); err != nil {
			return nil, "", err
		}
		return ApprovalResponse{PairingID: pairingID, Status: "approved", ClaimExpiresAt: wireTime(claimExpiresAt)}, principal.WorkspaceID, nil
	})
}

func (s *Service) ClaimPairing(ctx context.Context, clientIP, pairingID, claimSecret string) (Result, error) {
	if !secure.ValidUUID(pairingID) {
		return Result{}, ErrInvalid
	}
	if err := s.limit(ctx, RateRule{Scope: "pairing.claim.ip", Limit: 10, Window: 5 * time.Minute}, "ip:"+clientIP); err != nil {
		return Result{}, err
	}
	if err := s.limit(ctx, RateRule{Scope: "pairing.claim.id", Limit: 10, Window: 5 * time.Minute}, pairingID); err != nil {
		return Result{}, err
	}
	claimHash, err := secure.HashClaimSecret(claimSecret)
	if err != nil {
		return Result{}, ErrInvalidClaim
	}
	var body []byte
	err = s.store.WithinTx(ctx, func(tx TxStore) error {
		now := s.clock.Now()
		pairing, err := tx.LockPairingForClaim(ctx, pairingID, claimHash, now)
		if err != nil {
			return err
		}
		body, err = s.keyring.Decrypt("pairing-grant", pairingID, pairing.Grant)
		if err != nil {
			return err
		}
		return tx.MarkPairingClaimed(ctx, pairingID, now)
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Status: 200, Body: body}, nil
}

func (s *Service) ListDevices(ctx context.Context, principal Principal) ([]DeviceSummary, error) {
	if principal.Scope != "full" {
		return nil, ErrForbidden
	}
	var devices []Device
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		devices, err = tx.ListDevices(ctx, principal.WorkspaceID, principal.DeviceID)
		return err
	})
	if err != nil {
		return nil, err
	}
	summaries := make([]DeviceSummary, 0, len(devices))
	for _, device := range devices {
		summaries = append(summaries, deviceSummary(device))
	}
	return summaries, nil
}

func (s *Service) RenameDevice(ctx context.Context, principal Principal, deviceID, idempotencyKey string, input RenameInput) (Result, error) {
	if principal.Scope != "full" {
		return Result{}, ErrForbidden
	}
	if !secure.ValidUUID(deviceID) {
		return Result{}, ErrInvalid
	}
	name, err := NormalizeDisplayName(input.DisplayName)
	if err != nil {
		return Result{}, err
	}
	input.DisplayName = name
	canonical, _ := json.Marshal(input)
	return s.mutate(ctx, principal.WorkspaceID, "device.rename:"+deviceID, idempotencyKey, principal.WorkspaceID, canonical, 200, func(tx TxStore, now time.Time) (any, string, error) {
		device, err := tx.RenameDevice(ctx, principal.WorkspaceID, deviceID, name, now)
		if err != nil {
			return nil, "", err
		}
		if err := tx.InsertEvent(ctx, principal.WorkspaceID, "device.renamed", deviceID, now); err != nil {
			return nil, "", err
		}
		device.IsCurrent = device.ID == principal.DeviceID
		return struct {
			Device DeviceSummary `json:"device"`
		}{Device: deviceSummary(device)}, principal.WorkspaceID, nil
	})
}

func (s *Service) RevokeDevice(ctx context.Context, principal Principal, deviceID, idempotencyKey string) (Result, error) {
	if principal.Scope != "full" {
		return Result{}, ErrForbidden
	}
	if !secure.ValidUUID(deviceID) {
		return Result{}, ErrInvalid
	}
	if deviceID == principal.DeviceID {
		return Result{}, ErrInvalid
	}
	return s.mutate(ctx, principal.WorkspaceID, "device.revoke:"+deviceID, idempotencyKey, principal.WorkspaceID, []byte("{}"), 204, func(tx TxStore, now time.Time) (any, string, error) {
		if err := tx.RevokeDevice(ctx, principal.WorkspaceID, deviceID, now); err != nil {
			return nil, "", err
		}
		if err := tx.InsertEvent(ctx, principal.WorkspaceID, "device.revoked", deviceID, now); err != nil {
			return nil, "", err
		}
		return nil, principal.WorkspaceID, nil
	})
}

func (s *Service) Recover(ctx context.Context, clientIP, idempotencyKey string, input RecoveryInput) (Result, error) {
	name, err := NormalizeDisplayName(input.DeviceName)
	if err != nil || input.Platform != "macos" {
		return Result{}, ErrInvalid
	}
	workspaceID, locator, err := secure.RecoveryLocator(input.RecoveryCode)
	if err != nil {
		return Result{}, ErrInvalidRecovery
	}
	permit, err := secure.AcquireRecoveryPermit(ctx)
	if err != nil {
		return Result{}, err
	}
	defer permit.Release()
	input.DeviceName = name
	canonical, _ := json.Marshal(input)
	if replay, found, err := s.preflight(ctx, workspaceID, "recovery", idempotencyKey, workspaceID, canonical); err != nil || found {
		return replay, err
	}
	if err := s.limit(ctx, RateRule{Scope: "recovery.ip", Limit: 5, Window: 30 * time.Minute}, "ip:"+clientIP); err != nil {
		return Result{}, err
	}
	if err := s.limit(ctx, RateRule{Scope: "recovery.locator", Limit: 5, Window: 30 * time.Minute}, workspaceID+":"+locator); err != nil {
		return Result{}, err
	}
	rotated, err := secure.NewRecoveryWithPermit(ctx, permit, workspaceID, s.random)
	if err != nil {
		return Result{}, err
	}
	return s.mutate(ctx, workspaceID, "recovery", idempotencyKey, workspaceID, canonical, 201, func(tx TxStore, now time.Time) (any, string, error) {
		stored, err := tx.GetRecovery(ctx, workspaceID, locator)
		if errors.Is(err, ErrInvalidRecovery) {
			return nil, "", ErrInvalidRecovery
		}
		if err != nil {
			return nil, "", err
		}
		if secure.VerifyRecoveryWithPermit(ctx, permit, input.RecoveryCode, workspaceID, locator, stored.Verifier) != nil {
			return nil, "", ErrInvalidRecovery
		}
		deviceID, err := secure.NewUUID(s.random)
		if err != nil {
			return nil, "", err
		}
		device, err := tx.InsertDevice(ctx, workspaceID, Device{ID: deviceID, DisplayName: name, Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil {
			return nil, "", err
		}
		issued, records, err := s.issueCredentials(workspaceID, deviceID, "full", now)
		if err != nil {
			return nil, "", err
		}
		for _, record := range records {
			if err := tx.InsertCredential(ctx, workspaceID, record); err != nil {
				return nil, "", err
			}
		}
		if err := tx.PutRecovery(ctx, workspaceID, RecoveryRecord{WorkspaceID: workspaceID, Locator: rotated.Locator, Verifier: rotated.Verifier, CreatedAt: stored.CreatedAt, RotatedAt: now}); err != nil {
			return nil, "", err
		}
		if err := tx.InsertEvent(ctx, workspaceID, "device.added", deviceID, now); err != nil {
			return nil, "", err
		}
		if err := tx.InsertEvent(ctx, workspaceID, "recovery.rotated", deviceID, now); err != nil {
			return nil, "", err
		}
		return WorkspaceGrant{WorkspaceID: workspaceID, Device: grantDevice(device), Credentials: issued, RecoveryCode: rotated.Code}, workspaceID, nil
	})
}

func (s *Service) Cleanup(ctx context.Context) (CleanupResult, error) {
	return s.store.Cleanup(ctx, s.clock.Now())
}

func (s *Service) issueCredentials(workspaceID, deviceID, role string, now time.Time) ([]CredentialResponse, []CredentialRecord, error) {
	kinds := []string{"connector"}
	if role == "full" {
		kinds = []string{"full", "connector"}
	}
	responses := make([]CredentialResponse, 0, len(kinds))
	records := make([]CredentialRecord, 0, len(kinds))
	for _, kind := range kinds {
		issued, err := secure.NewCredential(workspaceID, kind, s.random)
		if err != nil {
			return nil, nil, err
		}
		responses = append(responses, CredentialResponse{Kind: kind, Token: issued.Token})
		records = append(records, CredentialRecord{DeviceID: deviceID, Locator: issued.Locator, Scope: kind, Hash: issued.Hash, CreatedAt: now})
	}
	return responses, records, nil
}

func (s *Service) preflight(ctx context.Context, scopeID, operation, key, workspaceID string, canonical []byte) (Result, bool, error) {
	keyHash, requestHash, err := idempotencyHashes(key, canonical)
	if err != nil {
		return Result{}, false, err
	}
	var record IdempotencyRecord
	err = s.store.WithinTx(ctx, func(tx TxStore) error {
		var lookupErr error
		record, lookupErr = tx.GetIdempotency(ctx, scopeID, operation, keyHash)
		return lookupErr
	})
	if errors.Is(err, ErrNotFound) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	if record.Expired {
		return Result{}, false, nil
	}
	result, err := s.decodeIdempotency(record, workspaceID, requestHash)
	return result, true, err
}

func (s *Service) mutate(ctx context.Context, scopeID, operation, key, workspaceID string, canonical []byte, status int, fn func(TxStore, time.Time) (any, string, error)) (Result, error) {
	keyHash, requestHash, err := idempotencyHashes(key, canonical)
	if err != nil {
		return Result{}, err
	}
	var result Result
	err = s.store.WithinTx(ctx, func(tx TxStore) error {
		if err := tx.LockIdempotency(ctx, scopeID, operation, keyHash); err != nil {
			return err
		}
		now := s.clock.Now()
		existing, err := tx.GetIdempotency(ctx, scopeID, operation, keyHash)
		if err == nil && !existing.Expired {
			decoded, err := s.decodeIdempotency(existing, workspaceID, requestHash)
			result = decoded
			return err
		}
		if err == nil {
			if err := tx.DeleteIdempotency(ctx, scopeID, operation, keyHash); err != nil {
				return err
			}
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		response, responseWorkspaceID, err := fn(tx, now)
		if err != nil {
			return err
		}
		var body []byte
		if response != nil {
			body, err = marshalLine(response)
			if err != nil {
				return err
			}
		}
		envelope, err := s.keyring.Encrypt("idempotency", scopeID+":"+operation+":"+hex.EncodeToString(keyHash), body)
		if err != nil {
			return err
		}
		record := IdempotencyRecord{
			ScopeID: scopeID, Operation: operation, KeyHash: keyHash, WorkspaceID: responseWorkspaceID,
			RequestHash: requestHash, Response: StoredResponse{Status: status, ContentType: "application/json", Envelope: envelope},
		}
		if err := tx.PutIdempotency(ctx, record); err != nil {
			return err
		}
		result = Result{Status: status, Body: body}
		return nil
	})
	return result, err
}

func (s *Service) decodeIdempotency(record IdempotencyRecord, workspaceID string, requestHash []byte) (Result, error) {
	if !bytes.Equal(record.RequestHash, requestHash) || workspaceID != "" && record.WorkspaceID != workspaceID {
		return Result{}, ErrIdempotencyConflict
	}
	body, err := s.keyring.Decrypt("idempotency", record.ScopeID+":"+record.Operation+":"+hex.EncodeToString(record.KeyHash), record.Response.Envelope)
	if err != nil {
		return Result{}, err
	}
	return Result{Status: record.Response.Status, Body: body}, nil
}

func (s *Service) limit(ctx context.Context, rule RateRule, subject string) error {
	hash := sha256.Sum256([]byte("mcpaste-rate-v1\x00" + rule.Scope + "\x00" + subject))
	decision, err := s.store.ConsumeRateLimit(ctx, rule, hash[:], s.clock.Now())
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return &RateLimitError{RetryAfter: decision.RetryAfter}
	}
	return nil
}

type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return ErrRateLimited.Error() }
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

func idempotencyHashes(key string, canonical []byte) ([]byte, []byte, error) {
	if !secure.ValidUUID(strings.ToLower(key)) || key != strings.ToLower(key) {
		return nil, nil, ErrInvalid
	}
	keyDigest := sha256.Sum256([]byte("mcpaste-idempotency-key-v1\x00" + key))
	requestHasher := sha256.New()
	_, _ = requestHasher.Write([]byte("mcpaste-idempotency-request-v1\x00"))
	_, _ = requestHasher.Write(canonical)
	return keyDigest[:], requestHasher.Sum(nil), nil
}

func marshalLine(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func details(pairing Pairing) PairingDetails {
	status := "pending"
	var claimExpiresAt *time.Time
	if pairing.WorkspaceID != "" {
		status = "approved"
		value := wireTime(pairing.ClaimExpiresAt)
		claimExpiresAt = &value
	}
	return PairingDetails{
		PairingID: pairing.ID, ProposedName: pairing.ProposedName, Platform: pairing.Platform,
		RequestedScope: pairing.RequestedScope, Status: status, ExpiresAt: wireTime(pairing.ExpiresAt),
		ClaimExpiresAt: claimExpiresAt,
	}
}

const shortCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

func validShortCode(value string) bool {
	if len(value) != 8 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if strings.IndexByte(shortCodeAlphabet, value[index]) < 0 {
			return false
		}
	}
	return true
}

func (s *Service) newShortCode() (string, error) {
	const accepted = 248
	var builder strings.Builder
	for builder.Len() < 8 {
		buffer := make([]byte, 1)
		if _, err := s.random.Read(buffer); err != nil {
			return "", err
		}
		if int(buffer[0]) >= accepted {
			continue
		}
		builder.WriteByte(shortCodeAlphabet[int(buffer[0])%len(shortCodeAlphabet)])
	}
	return builder.String(), nil
}

func RetryAfterSeconds(err error) (int, bool) {
	var rateError *RateLimitError
	if !errors.As(err, &rateError) {
		return 0, false
	}
	return int(math.Ceil(rateError.RetryAfter.Seconds())), true
}

```

The service returns raw infrastructure errors to the HTTP boundary, which maps them to generic metadata without logging the error string.

- [ ] **Step 5: Make pending pairing insertion collision-safe**

In `internal/identity/postgres/pairing.go`, replace `InsertPairing` with this complete version:

```go
func (s *txStore) InsertPairing(ctx context.Context, pairing identity.Pairing) error {
	command, err := s.tx.Exec(ctx, `
insert into pairing_requests(
    id, short_code, claim_hash, proposed_name, platform, requested_scope,
    created_at, expires_at, metadata_purge_at
) values ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9)
on conflict do nothing`,
		pairing.ID, pairing.ShortCode, pairing.ClaimHash, pairing.ProposedName,
		pairing.Platform, pairing.RequestedScope, pairing.CreatedAt,
		pairing.ExpiresAt, pairing.MetadataPurgeAt,
	)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return identity.ErrInvalid
	}
	return nil
}
```

- [ ] **Step 6: Run service tests green and all repository regression tests**

```bash
gofmt -w internal/secure/credential.go internal/secure/recovery.go internal/identity
export MCPASTE_TEST_DATABASE_URL='postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable'
go test -race ./internal/identity ./internal/identity/postgres
go vet ./internal/identity ./internal/identity/postgres
```

Expected: PASS. The workspace test proves exact credential count/order, hash-only persistence, and byte-identical idempotent replay. The fake-store unit test proves malformed short-code validation returns before rate limiting, `WithinTx`, and repository lookup; the PostgreSQL no-row test remains secondary integration evidence. The recovery permit test occupies both process permits, observes the third request waiting at permit admission, cancels it, and proves zero rate-limit and transaction calls; admitted recovery requests precompute the rotated verifier before mutation, enter at most two locked recovery transactions, re-read the row with `FOR UPDATE`, verify through the already-held typed permit, rotate transactionally, and release the permit on every return path.

- [ ] **Step 7: Commit transaction orchestration**

```bash
git add internal/secure/credential.go internal/secure/recovery.go internal/identity
git commit -m "feat: orchestrate encrypted identity issuance"
```

Expected: one commit containing service transactions, collision-safe pairing insertion, idempotency locking/replay, and tests.

## Task 10: Expose the strict standard-library HTTP API

**Files:**

- Create: `internal/httpserver/json.go`
- Create: `internal/httpserver/auth.go`
- Create: `internal/httpserver/clientip.go`
- Create: `internal/httpserver/api_test.go`
- Create: `internal/httpserver/api.go`
- Modify: `internal/httpserver/health.go`

- [ ] **Step 1: Write strict boundary tests first**

Create `internal/httpserver/api_test.go` with:

```go
package httpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

type fakeIdentityAPI struct {
	createCalls int
}

func (f *fakeIdentityAPI) Authenticate(_ context.Context, token string) (identity.Principal, error) {
	if token == "connector-runtime-marker" {
		return identity.Principal{WorkspaceID: "00000000-0000-4000-8000-000000000001", DeviceID: "00000000-0000-4000-8000-000000000002", Scope: "connector"}, nil
	}
	return identity.Principal{}, identity.ErrUnauthorized
}

func (f *fakeIdentityAPI) CreateWorkspace(_ context.Context, _, _ string, _ identity.CreateWorkspaceInput) (identity.Result, error) {
	f.createCalls++
	return identity.Result{Status: 201, Body: []byte("{\"workspace_id\":\"runtime-marker\"}\n")}, nil
}

func (f *fakeIdentityAPI) CreatePairing(context.Context, string, string, identity.CreatePairingInput) (identity.Result, error) {
	return identity.Result{}, identity.ErrNotFound
}
func (f *fakeIdentityAPI) PairingByID(context.Context, identity.Principal, string) (identity.PairingDetails, error) {
	return identity.PairingDetails{}, identity.ErrNotFound
}
func (f *fakeIdentityAPI) PairingByShortCode(context.Context, identity.Principal, string) (identity.PairingDetails, error) {
	return identity.PairingDetails{}, identity.ErrNotFound
}
func (f *fakeIdentityAPI) ApprovePairing(context.Context, identity.Principal, string, string) (identity.Result, error) {
	return identity.Result{}, identity.ErrNotFound
}
func (f *fakeIdentityAPI) ClaimPairing(context.Context, string, string, string) (identity.Result, error) {
	return identity.Result{}, identity.ErrNotFound
}
func (f *fakeIdentityAPI) ListDevices(context.Context, identity.Principal) ([]identity.DeviceSummary, error) {
	return nil, identity.ErrForbidden
}
func (f *fakeIdentityAPI) RenameDevice(context.Context, identity.Principal, string, string, identity.RenameInput) (identity.Result, error) {
	return identity.Result{}, identity.ErrForbidden
}
func (f *fakeIdentityAPI) RevokeDevice(context.Context, identity.Principal, string, string) (identity.Result, error) {
	return identity.Result{}, identity.ErrForbidden
}
func (f *fakeIdentityAPI) Recover(context.Context, string, string, identity.RecoveryInput) (identity.Result, error) {
	return identity.Result{}, identity.ErrInvalidRecovery
}

func TestWorkspaceCreateUsesStrictJSON(t *testing.T) {
	largeBody := `{"device_name":"` + strings.Repeat("a", 4060) + `","platform":"macos"}`
	if len(largeBody) != 4097 {
		t.Fatalf("large body length = %d", len(largeBody))
	}
	tests := []struct {
		name        string
		contentType string
		body        io.Reader
	}{
		{name: "unknown field", contentType: "application/json", body: strings.NewReader(`{"device_name":"Mac","platform":"macos","extra":true}`)},
		{name: "trailing value", contentType: "application/json", body: strings.NewReader(`{"device_name":"Mac","platform":"macos"}{}`)},
		{name: "null", contentType: "application/json", body: strings.NewReader(`null`)},
		{name: "array", contentType: "application/json", body: strings.NewReader(`[{"device_name":"Mac","platform":"macos"}]`)},
		{name: "wrong media type", contentType: "text/plain", body: strings.NewReader(`{"device_name":"Mac","platform":"macos"}`)},
		{name: "too large", contentType: "application/json", body: strings.NewReader(largeBody)},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			api := &fakeIdentityAPI{}
			request := httptest.NewRequest(http.MethodPost, "/v1/workspaces", item.body)
			request.Header.Set("Content-Type", item.contentType)
			request.Header.Set("Idempotency-Key", "00000000-0000-4000-8000-000000000901")
			response := httptest.NewRecorder()
			NewApplicationHandler(nil, api, nil).ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":{\"code\":\"invalid_request\"}}\n" {
				t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
			}
			if api.createCalls != 0 {
				t.Fatalf("createCalls = %d", api.createCalls)
			}
		})
	}
}

func TestConnectorCannotListDevices(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/devices", nil)
	request.Header.Set("Authorization", "Bearer connector-runtime-marker")
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, &fakeIdentityAPI{}, nil).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Body.String() != "{\"error\":{\"code\":\"forbidden\"}}\n" {
		t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
	}
}

func TestConnectorCannotRevokeDevice(t *testing.T) {
	request := httptest.NewRequest(http.MethodDelete, "/v1/devices/00000000-0000-4000-8000-000000000003", nil)
	request.Header.Set("Authorization", "Bearer connector-runtime-marker")
	request.Header.Set("Idempotency-Key", "00000000-0000-4000-8000-000000000904")
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, &fakeIdentityAPI{}, nil).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Body.String() != "{\"error\":{\"code\":\"forbidden\"}}\n" {
		t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
	}
}

func TestDuplicateSecurityHeadersAreRejected(t *testing.T) {
	t.Run("authorization", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/v1/devices", nil)
		request.Header.Add("Authorization", "Bearer connector-runtime-marker")
		request.Header.Add("Authorization", "Bearer second-runtime-marker")
		response := httptest.NewRecorder()
		NewApplicationHandler(nil, &fakeIdentityAPI{}, nil).ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || response.Body.String() != "{\"error\":{\"code\":\"unauthorized\"}}\n" {
			t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
		}
	})
	t.Run("idempotency", func(t *testing.T) {
		api := &fakeIdentityAPI{}
		request := httptest.NewRequest(http.MethodPost, "/v1/workspaces", strings.NewReader(`{"device_name":"Mac","platform":"macos"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Add("Idempotency-Key", "00000000-0000-4000-8000-000000000905")
		request.Header.Add("Idempotency-Key", "00000000-0000-4000-8000-000000000906")
		response := httptest.NewRecorder()
		NewApplicationHandler(nil, api, nil).ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":{\"code\":\"invalid_request\"}}\n" {
			t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
		}
		if api.createCalls != 0 {
			t.Fatalf("createCalls = %d", api.createCalls)
		}
	})
}

func TestV1MethodGuardRecognizesEveryRouteShape(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		path          string
		wantStatus    int
		wantAllow     string
		wantDelegated bool
	}{
		{name: "workspace create", method: http.MethodPost, path: "/v1/workspaces", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "pairing create", method: http.MethodPost, path: "/v1/pairing-requests", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "pairing lookup", method: http.MethodPost, path: "/v1/pairing-requests/lookup", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "pairing details", method: http.MethodGet, path: "/v1/pairing-requests/00000000-0000-4000-8000-000000000301", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "pairing approval", method: http.MethodPost, path: "/v1/pairing-requests/00000000-0000-4000-8000-000000000301/approve", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "pairing claim", method: http.MethodPost, path: "/v1/pairing-requests/00000000-0000-4000-8000-000000000301/claim", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "device list", method: http.MethodGet, path: "/v1/devices", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "device rename", method: http.MethodPatch, path: "/v1/devices/00000000-0000-4000-8000-000000000201", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "device revoke", method: http.MethodDelete, path: "/v1/devices/00000000-0000-4000-8000-000000000201", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "recovery", method: http.MethodPost, path: "/v1/recoveries", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "non v1 health", method: http.MethodGet, path: "/readyz", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "workspace wrong method", method: http.MethodGet, path: "/v1/workspaces", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodPost},
		{name: "pairing details head", method: http.MethodHead, path: "/v1/pairing-requests/00000000-0000-4000-8000-000000000301", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodGet},
		{name: "device list head", method: http.MethodHead, path: "/v1/devices", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodGet},
		{name: "device dynamic wrong method", method: http.MethodGet, path: "/v1/devices/00000000-0000-4000-8000-000000000201", wantStatus: http.StatusMethodNotAllowed, wantAllow: "PATCH, DELETE"},
		{name: "static lookup is not dynamic details", method: http.MethodGet, path: "/v1/pairing-requests/lookup", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodPost},
		{name: "unknown path", method: http.MethodGet, path: "/v1/not-a-route", wantStatus: http.StatusNotFound},
		{name: "extra dynamic segment", method: http.MethodGet, path: "/v1/devices/id/extra", wantStatus: http.StatusNotFound},
		{name: "v1 root", method: http.MethodGet, path: "/v1/", wantStatus: http.StatusNotFound},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			delegated := 0
			handler := v1MethodGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				delegated++
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(item.method, item.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != item.wantStatus || response.Header().Get("Allow") != item.wantAllow {
				t.Fatalf("status/Allow = %d/%q", response.Code, response.Header().Get("Allow"))
			}
			if item.wantDelegated {
				if delegated != 1 || response.Body.Len() != 0 {
					t.Fatalf("delegated/body bytes = %d/%d", delegated, response.Body.Len())
				}
				return
			}
			wantBody := "{\"error\":{\"code\":\"invalid_request\"}}\n"
			if item.wantStatus == http.StatusNotFound {
				wantBody = "{\"error\":{\"code\":\"not_found\"}}\n"
			}
			if delegated != 0 || response.Body.String() != wantBody {
				t.Fatalf("delegated/body metadata = %d/%d", delegated, response.Body.Len())
			}
		})
	}
}
```

- [ ] **Step 2: Run API tests red**

```bash
go test ./internal/httpserver -run 'TestWorkspaceCreate|TestConnector|TestDuplicate|TestV1MethodGuard'
```

Expected: FAIL because `NewApplicationHandler` is undefined.

- [ ] **Step 3: Write strict JSON and stable error helpers**

Create `internal/httpserver/json.go` with:

```go
package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return identityInvalid()
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return identityInvalid()
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return identityInvalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return identityInvalid()
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return identityInvalid()
	}
	return nil
}

func requireEmptyBody(r *http.Request) error {
	if r.Body == nil {
		return nil
	}
	buffer := make([]byte, 1)
	count, err := r.Body.Read(buffer)
	if count != 0 || err != io.EOF {
		return identityInvalid()
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeResult(w http.ResponseWriter, result identity.Result) {
	if result.Status == http.StatusNoContent {
		w.WriteHeader(result.Status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(result.Status)
	_, _ = w.Write(result.Body)
}

func identityInvalid() error { return identity.ErrInvalid }
```

- [ ] **Step 4: Write exact bearer parsing and scope enforcement**

Create `internal/httpserver/auth.go` with:

```go
package httpserver

import (
	"context"
	"net/http"
	"strings"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

type identityAPI interface {
	Authenticate(context.Context, string) (identity.Principal, error)
	CreateWorkspace(context.Context, string, string, identity.CreateWorkspaceInput) (identity.Result, error)
	CreatePairing(context.Context, string, string, identity.CreatePairingInput) (identity.Result, error)
	PairingByID(context.Context, identity.Principal, string) (identity.PairingDetails, error)
	PairingByShortCode(context.Context, identity.Principal, string) (identity.PairingDetails, error)
	ApprovePairing(context.Context, identity.Principal, string, string) (identity.Result, error)
	ClaimPairing(context.Context, string, string, string) (identity.Result, error)
	ListDevices(context.Context, identity.Principal) ([]identity.DeviceSummary, error)
	RenameDevice(context.Context, identity.Principal, string, string, identity.RenameInput) (identity.Result, error)
	RevokeDevice(context.Context, identity.Principal, string, string) (identity.Result, error)
	Recover(context.Context, string, string, identity.RecoveryInput) (identity.Result, error)
}

func authenticate(r *http.Request, service identityAPI) (identity.Principal, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || strings.Count(values[0], " ") != 1 {
		return identity.Principal{}, identity.ErrUnauthorized
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if token == "" {
		return identity.Principal{}, identity.ErrUnauthorized
	}
	return service.Authenticate(r.Context(), token)
}

func requireFull(principal identity.Principal) error {
	if principal.Scope != "full" {
		return identity.ErrForbidden
	}
	return nil
}
```

- [ ] **Step 5: Write trusted-proxy client IP extraction**

Create `internal/httpserver/clientip.go` with:

```go
package httpserver

import (
	"net"
	"net/http"
	"strings"
)

func clientIP(r *http.Request, trusted []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote := net.ParseIP(host)
	if remote == nil || !inNetworks(remote, trusted) {
		return host
	}
	values := r.Header.Values("X-Forwarded-For")
	if len(values) != 1 {
		return host
	}
	parts := strings.Split(values[0], ",")
	for index := len(parts) - 1; index >= 0; index-- {
		candidate := net.ParseIP(strings.TrimSpace(parts[index]))
		if candidate == nil {
			return host
		}
		if !inNetworks(candidate, trusted) {
			return candidate.String()
		}
	}
	return host
}

func inNetworks(ip net.IP, networks []*net.IPNet) bool {
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 6: Refactor health registration without changing responses**

Replace only `NewHandler` in `internal/httpserver/health.go` with these two functions:

```go
func NewHandler(readiness ReadinessFunc) http.Handler {
	mux := http.NewServeMux()
	registerHealth(mux, readiness)
	return mux
}

func registerHealth(mux *http.ServeMux, readiness ReadinessFunc) {
	mux.HandleFunc("/livez", healthHandler(nil))
	mux.HandleFunc("/readyz", healthHandler(readiness))
}
```

Do not change `healthHandler` or `writeHealth`.

- [ ] **Step 7: Write all method-aware routes and handlers**

Create `internal/httpserver/api.go` with:

```go
package httpserver

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

type apiServer struct {
	identity identityAPI
	proxies  []*net.IPNet
}

func NewApplicationHandler(readiness ReadinessFunc, service identityAPI, proxies []*net.IPNet) http.Handler {
	server := &apiServer{identity: service, proxies: proxies}
	mux := http.NewServeMux()
	registerHealth(mux, readiness)
	mux.HandleFunc("POST /v1/workspaces", server.createWorkspace)
	mux.HandleFunc("POST /v1/pairing-requests", server.createPairing)
	mux.HandleFunc("POST /v1/pairing-requests/lookup", server.lookupPairing)
	mux.HandleFunc("GET /v1/pairing-requests/{pairing_id}", server.getPairing)
	mux.HandleFunc("POST /v1/pairing-requests/{pairing_id}/approve", server.approvePairing)
	mux.HandleFunc("POST /v1/pairing-requests/{pairing_id}/claim", server.claimPairing)
	mux.HandleFunc("GET /v1/devices", server.listDevices)
	mux.HandleFunc("PATCH /v1/devices/{device_id}", server.renameDevice)
	mux.HandleFunc("DELETE /v1/devices/{device_id}", server.revokeDevice)
	mux.HandleFunc("POST /v1/recoveries", server.recover)
	return v1MethodGuard(mux)
}

func v1MethodGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1" && !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		methods, known := v1RouteMethods(r.URL.Path)
		if !known {
			writeError(w, identity.ErrNotFound)
			return
		}
		for _, method := range methods {
			if r.Method == method {
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"error": map[string]string{"code": "invalid_request"},
		})
	})
}

func v1RouteMethods(path string) ([]string, bool) {
	switch path {
	case "/v1/workspaces", "/v1/pairing-requests", "/v1/pairing-requests/lookup", "/v1/recoveries":
		return []string{http.MethodPost}, true
	case "/v1/devices":
		return []string{http.MethodGet}, true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/"), "/")
	if len(parts) == 2 && parts[0] == "pairing-requests" && parts[1] != "" {
		return []string{http.MethodGet}, true
	}
	if len(parts) == 3 && parts[0] == "pairing-requests" && parts[1] != "" {
		switch parts[2] {
		case "approve", "claim":
			return []string{http.MethodPost}, true
		}
	}
	if len(parts) == 2 && parts[0] == "devices" && parts[1] != "" {
		return []string{http.MethodPatch, http.MethodDelete}, true
	}
	return nil, false
}

func (s *apiServer) createWorkspace(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, err := oneHeader(r, "Idempotency-Key")
	if err != nil {
		writeError(w, err)
		return
	}
	var input identity.CreateWorkspaceInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.CreateWorkspace(r.Context(), clientIP(r, s.proxies), idempotencyKey, input)
	writeResultOrError(w, result, err)
}

func (s *apiServer) createPairing(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, err := oneHeader(r, "Idempotency-Key")
	if err != nil {
		writeError(w, err)
		return
	}
	var input identity.CreatePairingInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.CreatePairing(r.Context(), clientIP(r, s.proxies), idempotencyKey, input)
	writeResultOrError(w, result, err)
}

func (s *apiServer) getPairing(w http.ResponseWriter, r *http.Request) {
	err := requireEmptyBody(r)
	var principal identity.Principal
	if err == nil {
		principal, err = authenticate(r, s.identity)
	}
	if err == nil {
		err = requireFull(principal)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	details, err := s.identity.PairingByID(r.Context(), principal, r.PathValue("pairing_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, details)
}

func (s *apiServer) lookupPairing(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	var input struct {
		ShortCode string `json:"short_code"`
	}
	if err == nil {
		err = decodeJSON(w, r, &input)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	details, err := s.identity.PairingByShortCode(r.Context(), principal, input.ShortCode)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, details)
}

func (s *apiServer) approvePairing(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	var idempotencyKey string
	if err == nil {
		idempotencyKey, err = oneHeader(r, "Idempotency-Key")
	}
	var input struct{}
	if err == nil {
		err = decodeJSON(w, r, &input)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.ApprovePairing(r.Context(), principal, r.PathValue("pairing_id"), idempotencyKey)
	writeResultOrError(w, result, err)
}

func (s *apiServer) claimPairing(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ClaimSecret string `json:"claim_secret"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.ClaimPairing(r.Context(), clientIP(r, s.proxies), r.PathValue("pairing_id"), input.ClaimSecret)
	writeResultOrError(w, result, err)
}

func (s *apiServer) listDevices(w http.ResponseWriter, r *http.Request) {
	err := requireEmptyBody(r)
	var principal identity.Principal
	if err == nil {
		principal, err = authenticate(r, s.identity)
	}
	if err == nil {
		err = requireFull(principal)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	devices, err := s.identity.ListDevices(r.Context(), principal)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Devices []identity.DeviceSummary `json:"devices"`
	}{Devices: devices})
}

func (s *apiServer) renameDevice(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	var idempotencyKey string
	if err == nil {
		idempotencyKey, err = oneHeader(r, "Idempotency-Key")
	}
	var input identity.RenameInput
	if err == nil {
		err = decodeJSON(w, r, &input)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.RenameDevice(r.Context(), principal, r.PathValue("device_id"), idempotencyKey, input)
	writeResultOrError(w, result, err)
}

func (s *apiServer) revokeDevice(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	if err == nil {
		err = requireEmptyBody(r)
	}
	var idempotencyKey string
	if err == nil {
		idempotencyKey, err = oneHeader(r, "Idempotency-Key")
	}
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.RevokeDevice(r.Context(), principal, r.PathValue("device_id"), idempotencyKey)
	writeResultOrError(w, result, err)
}

func (s *apiServer) recover(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, err := oneHeader(r, "Idempotency-Key")
	if err != nil {
		writeError(w, err)
		return
	}
	var input identity.RecoveryInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.Recover(r.Context(), clientIP(r, s.proxies), idempotencyKey, input)
	writeResultOrError(w, result, err)
}

func oneHeader(r *http.Request, name string) (string, error) {
	values := r.Header.Values(name)
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", identity.ErrInvalid
	}
	return values[0], nil
}

func writeResultOrError(w http.ResponseWriter, result identity.Result, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeResult(w, result)
}

func writeError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, identity.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, identity.ErrUnauthorized):
		w.Header().Set("WWW-Authenticate", "Bearer")
		status, code = http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, identity.ErrForbidden):
		status, code = http.StatusForbidden, "forbidden"
	case errors.Is(err, identity.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, identity.ErrIdempotencyConflict):
		status, code = http.StatusConflict, "idempotency_conflict"
	case errors.Is(err, identity.ErrPairingPending):
		status, code = http.StatusConflict, "pairing_pending"
	case errors.Is(err, identity.ErrPairingApproved):
		status, code = http.StatusConflict, "pairing_already_approved"
	case errors.Is(err, identity.ErrPairingExpired):
		status, code = http.StatusGone, "pairing_expired"
	case errors.Is(err, identity.ErrInvalidClaim):
		status, code = http.StatusUnauthorized, "invalid_claim"
	case errors.Is(err, identity.ErrInvalidRecovery):
		status, code = http.StatusUnauthorized, "invalid_recovery"
	case errors.Is(err, identity.ErrRateLimited):
		status, code = http.StatusTooManyRequests, "rate_limited"
		if seconds, ok := identity.RetryAfterSeconds(err); ok {
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
		}
	}
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code}})
}
```

- [ ] **Step 8: Make API tests green and preserve Foundation health tests**

```bash
gofmt -w internal/httpserver
go test -race ./internal/httpserver
go vet ./internal/httpserver
```

Expected: all API and existing health/logging tests pass. Foundation `/livez`, `/readyz`, panic recovery, status capture, and safe route-pattern logging remain unchanged.

- [ ] **Step 9: Add client-IP trust tests**

Replace the import block in `internal/httpserver/api_test.go` only now, when `net` is first used:

```go
import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/identity"
)
```

Append to `internal/httpserver/api_test.go`:

```go
func TestClientIPTrustsForwardingOnlyFromConfiguredProxy(t *testing.T) {
	_, trusted, err := net.ParseCIDR("127.0.0.1/32")
	if err != nil {
		t.Fatalf("ParseCIDR() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "127.0.0.1:4000"
	request.Header.Set("X-Forwarded-For", "192.0.2.44, 127.0.0.1")
	if got := clientIP(request, []*net.IPNet{trusted}); got != "192.0.2.44" {
		t.Fatalf("trusted clientIP = %q", got)
	}
	request.RemoteAddr = "198.51.100.9:4000"
	if got := clientIP(request, []*net.IPNet{trusted}); got != "198.51.100.9" {
		t.Fatalf("untrusted clientIP = %q", got)
	}
}
```

```bash
gofmt -w internal/httpserver/api_test.go
go test -race ./internal/httpserver -run TestClientIP
```

Expected: PASS.

- [ ] **Step 10: Commit the HTTP contract**

```bash
git add internal/httpserver
git commit -m "feat: expose identity HTTP API"
```

Expected: one commit with method-aware standard-library routes, strict 4 KiB JSON, auth/scope handling, proxy-safe IP extraction, and stable generic errors.

## Task 11: Prove the complete identity lifecycle through HTTP and PostgreSQL

**Files:**

- Create: `internal/httpserver/identity_integration_test.go`
- Modify: `internal/httpserver/logging.go`
- Modify: `internal/httpserver/logging_test.go`
- Modify: `internal/httpserver/health_test.go`

- [ ] **Step 1: Create the integration harness and lifecycle test**

Create `internal/httpserver/identity_integration_test.go` with:

```go
package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/database"
	"github.com/1yoouoo/mcpaste/internal/identity"
	identitypostgres "github.com/1yoouoo/mcpaste/internal/identity/postgres"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/1yoouoo/mcpaste/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

type mutableClock struct {
	mu    sync.Mutex
	value time.Time
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func (c *mutableClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.value = c.value.Add(duration)
	c.mu.Unlock()
}

type deterministicReader struct {
	mu       sync.Mutex
	counter  uint64
	buffered []byte
}

func (r *deterministicReader) Read(target []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	written := 0
	for written < len(target) {
		if len(r.buffered) == 0 {
			var encodedCounter [8]byte
			binary.BigEndian.PutUint64(encodedCounter[:], r.counter)
			hasher := sha256.New()
			_, _ = hasher.Write([]byte("mcpaste-integration-test-random-v1\x00"))
			_, _ = hasher.Write(encodedCounter[:])
			r.buffered = hasher.Sum(nil)
			r.counter++
		}
		copied := copy(target[written:], r.buffered)
		written += copied
		r.buffered = r.buffered[copied:]
	}
	return written, nil
}

func TestDeterministicReaderDoesNotRepeatBlocks(t *testing.T) {
	reader := &deterministicReader{}
	output := make([]byte, 4096)
	read, err := reader.Read(output)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if read != len(output) {
		t.Fatalf("Read() bytes = %d", read)
	}
	seen := make(map[[sha256.Size]byte]struct{}, len(output)/sha256.Size)
	for offset := 0; offset < len(output); offset += sha256.Size {
		var block [sha256.Size]byte
		copy(block[:], output[offset:offset+sha256.Size])
		if _, exists := seen[block]; exists {
			t.Fatalf("deterministic stream repeated block index %d", offset/sha256.Size)
		}
		seen[block] = struct{}{}
	}
}

type integrationHarness struct {
	t       *testing.T
	pool    *pgxpool.Pool
	clock   *mutableClock
	handler http.Handler
	logs    *bytes.Buffer
	key     int
}

func newIntegrationHarness(t *testing.T) *integrationHarness {
	t.Helper()
	pool := testdb.New(t)
	random := &deterministicReader{}
	keyring, err := secure.NewKeyring("test-key", map[string][]byte{"test-key": bytes.Repeat([]byte{0x31}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	clock := &mutableClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	service := identity.NewService(identitypostgres.New(pool), keyring, random, clock)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	application := NewApplicationHandler(func(ctx context.Context) error { return database.Ready(ctx, pool) }, service, nil)
	handler := NewRecoveryMiddleware(logger)(NewAccessLogMiddleware(logger)(application))
	return &integrationHarness{t: t, pool: pool, clock: clock, handler: handler, logs: &logs}
}

func (h *integrationHarness) nextKey() string {
	h.key++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", h.key)
}

func (h *integrationHarness) request(method, path, bearer, idempotencyKey string, input any) (int, http.Header, []byte) {
	h.t.Helper()
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			h.t.Fatalf("marshal request: %v", err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.RemoteAddr = "192.0.2.20:4000"
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, request)
	return response.Code, response.Header(), response.Body.Bytes()
}

func (h *integrationHarness) createWorkspace(name string) (identity.WorkspaceGrant, string, []byte) {
	h.t.Helper()
	key := h.nextKey()
	status, _, body := h.request(http.MethodPost, "/v1/workspaces", "", key, map[string]any{"device_name": name, "platform": "macos"})
	if status != http.StatusCreated {
		h.t.Fatalf("workspace create status = %d", status)
	}
	var grant identity.WorkspaceGrant
	if err := json.Unmarshal(body, &grant); err != nil {
		h.t.Fatalf("decode workspace grant: %v", err)
	}
	return grant, key, bytes.Clone(body)
}

func credential(grant identity.WorkspaceGrant, kind string) string {
	for _, item := range grant.Credentials {
		if item.Kind == kind {
			return item.Token
		}
	}
	return ""
}

func TestIdentityLifecycleIntegration(t *testing.T) {
	h := newIntegrationHarness(t)
	workspace, workspaceKey, workspaceBody := h.createWorkspace("MacBook Pro")
	if len(workspace.Credentials) != 2 || workspace.Credentials[0].Kind != "full" || workspace.Credentials[1].Kind != "connector" {
		t.Fatalf("initial credential kinds are incorrect")
	}
	status, _, replayBody := h.request(http.MethodPost, "/v1/workspaces", "", workspaceKey, map[string]any{"device_name": "MacBook Pro", "platform": "macos"})
	if status != http.StatusCreated || !bytes.Equal(workspaceBody, replayBody) {
		t.Fatal("workspace idempotent replay differs")
	}
	fullToken := credential(workspace, "full")
	connectorToken := credential(workspace, "connector")
	if fullToken == "" || connectorToken == "" || fullToken == connectorToken {
		t.Fatal("separate initial credentials were not returned")
	}

	pairKey := h.nextKey()
	status, _, pairBody := h.request(http.MethodPost, "/v1/pairing-requests", "", pairKey, map[string]any{
		"proposed_name": "macbook pro", "platform": "linux", "requested_scope": "connector",
	})
	if status != http.StatusCreated {
		t.Fatalf("connector pairing create status = %d", status)
	}
	var pairing identity.PairingCreateResponse
	if err := json.Unmarshal(pairBody, &pairing); err != nil {
		t.Fatalf("decode pairing: %v", err)
	}
	if pairing.QRPayload != "mcpaste://pair/"+pairing.PairingID || strings.Contains(pairing.QRPayload, pairing.ClaimSecret) {
		t.Fatal("QR payload contains data beyond the pending identifier")
	}
	status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/claim", "", "", map[string]any{"claim_secret": pairing.ClaimSecret})
	if status != http.StatusConflict {
		t.Fatalf("pending claim status = %d", status)
	}
	status, _, detailsByID := h.request(http.MethodGet, "/v1/pairing-requests/"+pairing.PairingID, fullToken, "", nil)
	if status != http.StatusOK || bytes.Contains(detailsByID, []byte(pairing.ClaimSecret)) || !bytes.Contains(detailsByID, []byte(`"status":"pending"`)) {
		t.Fatalf("pairing detail status/leak = %d", status)
	}
	status, malformedHeaders, malformedBody := h.request(http.MethodGet, "/v1/pairing-requests/not-a-uuid", fullToken, "", nil)
	if status != http.StatusBadRequest || malformedHeaders.Get("Content-Type") != "application/json" || string(malformedBody) != "{\"error\":{\"code\":\"invalid_request\"}}\n" {
		t.Fatalf("malformed pairing UUID response metadata = %d/%q/%d", status, malformedHeaders.Get("Content-Type"), len(malformedBody))
	}
	status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/lookup", fullToken, "", map[string]any{"short_code": "I2345678"})
	if status != http.StatusBadRequest {
		t.Fatalf("malformed short-code status = %d", status)
	}
	var malformedLookupRateRows int
	if err := h.pool.QueryRow(context.Background(), `
select count(*) from rate_limit_buckets where scope = 'pairing.lookup'`).Scan(&malformedLookupRateRows); err != nil {
		t.Fatalf("count malformed lookup rate rows: %v", err)
	}
	if malformedLookupRateRows != 0 {
		t.Fatalf("malformed lookup rate rows = %d", malformedLookupRateRows)
	}
	status, _, detailsBody := h.request(http.MethodPost, "/v1/pairing-requests/lookup", fullToken, "", map[string]any{"short_code": pairing.ShortCode})
	if status != http.StatusOK || bytes.Contains(detailsBody, []byte(pairing.ClaimSecret)) {
		t.Fatalf("pairing lookup status/leak = %d", status)
	}
	status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/approve", connectorToken, h.nextKey(), map[string]any{})
	if status != http.StatusForbidden {
		t.Fatalf("connector approval status = %d", status)
	}
	status, _, _ = h.request(http.MethodPatch, "/v1/devices/"+workspace.Device.ID, connectorToken, h.nextKey(), map[string]any{"display_name": "Forbidden Rename"})
	if status != http.StatusForbidden {
		t.Fatalf("connector rename status = %d", status)
	}
	approvalKey := h.nextKey()
	status, _, approvalBody := h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/approve", fullToken, approvalKey, map[string]any{})
	if status != http.StatusOK || bytes.Contains(approvalBody, []byte(pairing.ClaimSecret)) || bytes.Contains(approvalBody, []byte("mcp1.")) {
		t.Fatalf("approval status or response leak = %d", status)
	}
	status, _, approvalReplay := h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/approve", fullToken, approvalKey, map[string]any{})
	if status != http.StatusOK || !bytes.Equal(approvalBody, approvalReplay) {
		t.Fatal("approval idempotent replay was not byte-identical")
	}
	var devicesAfterApproval int
	if err := h.pool.QueryRow(context.Background(), `
select count(*) from devices where workspace_id = $1::uuid`, workspace.WorkspaceID).Scan(&devicesAfterApproval); err != nil {
		t.Fatalf("count devices after approval replay: %v", err)
	}
	if devicesAfterApproval != 2 {
		t.Fatalf("devices after approval replay = %d", devicesAfterApproval)
	}
	status, _, firstClaim := h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/claim", "", "", map[string]any{"claim_secret": pairing.ClaimSecret})
	if status != http.StatusOK {
		t.Fatalf("first claim status = %d", status)
	}
	status, _, secondClaim := h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/claim", "", "", map[string]any{"claim_secret": pairing.ClaimSecret})
	if status != http.StatusOK || !bytes.Equal(firstClaim, secondClaim) {
		t.Fatal("claim replay changed credentials")
	}
	var connectorGrant identity.WorkspaceGrant
	if err := json.Unmarshal(firstClaim, &connectorGrant); err != nil {
		t.Fatalf("decode connector grant: %v", err)
	}
	if len(connectorGrant.Credentials) != 1 || connectorGrant.Credentials[0].Kind != "connector" || connectorGrant.Device.DisplayName != "macbook pro (2)" {
		t.Fatal("connector grant count, scope, or suffix is incorrect")
	}
	status, _, _ = h.request(http.MethodGet, "/v1/devices", connectorGrant.Credentials[0].Token, "", nil)
	if status != http.StatusForbidden {
		t.Fatalf("connector device administration status = %d", status)
	}

	fullPairKey := h.nextKey()
	status, _, fullPairBody := h.request(http.MethodPost, "/v1/pairing-requests", "", fullPairKey, map[string]any{
		"proposed_name": "MacBook Pro", "platform": "macos", "requested_scope": "full",
	})
	if status != http.StatusCreated {
		t.Fatalf("full pairing create status = %d", status)
	}
	var fullPair identity.PairingCreateResponse
	if err := json.Unmarshal(fullPairBody, &fullPair); err != nil {
		t.Fatalf("decode full pairing: %v", err)
	}
	status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+fullPair.PairingID+"/approve", fullToken, h.nextKey(), map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("full approval status = %d", status)
	}
	status, _, fullClaimBody := h.request(http.MethodPost, "/v1/pairing-requests/"+fullPair.PairingID+"/claim", "", "", map[string]any{"claim_secret": fullPair.ClaimSecret})
	if status != http.StatusOK {
		t.Fatalf("full claim status = %d", status)
	}
	var joinedFull identity.WorkspaceGrant
	if err := json.Unmarshal(fullClaimBody, &joinedFull); err != nil {
		t.Fatalf("decode full grant: %v", err)
	}
	if len(joinedFull.Credentials) != 2 || joinedFull.Credentials[0].Kind != "full" || joinedFull.Credentials[1].Kind != "connector" || joinedFull.Device.DisplayName != "MacBook Pro (3)" {
		t.Fatal("full pairing credential count, order, or suffix is incorrect")
	}
	joinedFullToken := credential(joinedFull, "full")
	if joinedFullToken == "" {
		t.Fatal("joined full credential is missing")
	}
	status, _, _ = h.request(http.MethodDelete, "/v1/devices/"+workspace.Device.ID, fullToken, h.nextKey(), nil)
	if status != http.StatusBadRequest {
		t.Fatalf("self-revocation status = %d", status)
	}
	var selfRevokeRows int
	if err := h.pool.QueryRow(context.Background(), `
select count(*) from idempotency_records where scope_id = $1 and operation = $2`, workspace.WorkspaceID, "device.revoke:"+workspace.Device.ID).Scan(&selfRevokeRows); err != nil {
		t.Fatalf("count self-revocation idempotency rows: %v", err)
	}
	if selfRevokeRows != 0 {
		t.Fatalf("self-revocation idempotency rows = %d", selfRevokeRows)
	}
	status, _, _ = h.request(http.MethodGet, "/v1/devices", fullToken, "", nil)
	if status != http.StatusOK {
		t.Fatalf("self-revocation mutated current device: %d", status)
	}

	status, _, devicesBody := h.request(http.MethodGet, "/v1/devices", fullToken, "", nil)
	if status != http.StatusOK {
		t.Fatalf("device list status = %d", status)
	}
	var list struct {
		Devices []identity.DeviceSummary `json:"devices"`
	}
	if err := json.Unmarshal(devicesBody, &list); err != nil || len(list.Devices) != 3 {
		t.Fatalf("device list count/decode = %d/%v", len(list.Devices), err)
	}
	status, _, renameBody := h.request(http.MethodPatch, "/v1/devices/"+connectorGrant.Device.ID, fullToken, h.nextKey(), map[string]any{"display_name": "MACBOOK PRO"})
	if status != http.StatusOK || !bytes.Contains(renameBody, []byte(`"display_name":"MACBOOK PRO (2)"`)) {
		t.Fatalf("duplicate rename status/result = %d", status)
	}
	revokeKey := h.nextKey()
	status, _, _ = h.request(http.MethodDelete, "/v1/devices/"+connectorGrant.Device.ID, connectorToken, h.nextKey(), nil)
	if status != http.StatusForbidden {
		t.Fatalf("connector revocation status = %d", status)
	}
	status, _, _ = h.request(http.MethodDelete, "/v1/devices/"+connectorGrant.Device.ID, fullToken, revokeKey, nil)
	if status != http.StatusNoContent {
		t.Fatalf("revoke status = %d", status)
	}
	status, _, _ = h.request(http.MethodDelete, "/v1/devices/"+connectorGrant.Device.ID, fullToken, revokeKey, nil)
	if status != http.StatusNoContent {
		t.Fatalf("revoke replay status = %d", status)
	}
	status, _, _ = h.request(http.MethodGet, "/v1/devices", connectorGrant.Credentials[0].Token, "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked auth status = %d", status)
	}

	other, _, _ := h.createWorkspace("Other Mac")
	otherFull := credential(other, "full")
	status, _, _ = h.request(http.MethodPatch, "/v1/devices/"+workspace.Device.ID, otherFull, h.nextKey(), map[string]any{"display_name": "Cross Workspace"})
	if status != http.StatusNotFound {
		t.Fatalf("cross-workspace rename status = %d", status)
	}
	status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+fullPair.PairingID+"/approve", otherFull, h.nextKey(), map[string]any{})
	if status != http.StatusNotFound {
		t.Fatalf("cross-workspace pairing status = %d", status)
	}

	recoveryKey := h.nextKey()
	status, _, recoveryBody := h.request(http.MethodPost, "/v1/recoveries", "", recoveryKey, map[string]any{
		"recovery_code": workspace.RecoveryCode, "device_name": "Recovered Mac", "platform": "macos",
	})
	if status != http.StatusCreated {
		t.Fatalf("recovery status = %d", status)
	}
	status, _, recoveryReplay := h.request(http.MethodPost, "/v1/recoveries", "", recoveryKey, map[string]any{
		"recovery_code": workspace.RecoveryCode, "device_name": "Recovered Mac", "platform": "macos",
	})
	if status != http.StatusCreated || !bytes.Equal(recoveryBody, recoveryReplay) {
		t.Fatal("recovery idempotent replay differs")
	}
	var recovered identity.WorkspaceGrant
	if err := json.Unmarshal(recoveryBody, &recovered); err != nil {
		t.Fatalf("decode recovery grant: %v", err)
	}
	if recovered.WorkspaceID != workspace.WorkspaceID || recovered.RecoveryCode == workspace.RecoveryCode || len(recovered.Credentials) != 2 {
		t.Fatal("recovery did not rotate or issue exact full credentials")
	}
	status, _, _ = h.request(http.MethodGet, "/v1/devices", fullToken, "", nil)
	if status != http.StatusOK {
		t.Fatalf("existing full device not preserved: %d", status)
	}
	status, _, _ = h.request(http.MethodPost, "/v1/recoveries", "", h.nextKey(), map[string]any{
		"recovery_code": workspace.RecoveryCode, "device_name": "Old Code", "platform": "macos",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("old recovery code status = %d", status)
	}
	if _, err := h.pool.Exec(context.Background(), `
update recovery_verifiers set verifier = decode(repeat('ff', 32), 'hex')
where workspace_id = $1::uuid`, workspace.WorkspaceID); err != nil {
		t.Fatalf("corrupt verifier: %v", err)
	}
	status, _, _ = h.request(http.MethodPost, "/v1/recoveries", "", h.nextKey(), map[string]any{
		"recovery_code": recovered.RecoveryCode, "device_name": "Corrupt Verifier", "platform": "macos",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("corrupt verifier recovery status = %d", status)
	}
	status, _, _ = h.request(http.MethodDelete, "/v1/devices/"+workspace.Device.ID, joinedFullToken, h.nextKey(), nil)
	if status != http.StatusNoContent {
		t.Fatalf("other-full-device revocation status = %d", status)
	}
	status, _, _ = h.request(http.MethodGet, "/v1/devices", fullToken, "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("other-full-device revocation auth status = %d", status)
	}

	var claimHashLength int
	if err := h.pool.QueryRow(context.Background(), `
select octet_length(claim_hash) from pairing_requests where id = $1::uuid`, pairing.PairingID).Scan(&claimHashLength); err != nil {
		t.Fatalf("inspect claim hash: %v", err)
	}
	if claimHashLength != 32 {
		t.Fatalf("claim hash length = %d", claimHashLength)
	}
	for _, marker := range []string{pairing.ClaimSecret, pairing.ShortCode, pairing.QRPayload, fullToken, connectorToken, workspace.RecoveryCode} {
		if strings.Contains(h.logs.String(), marker) {
			t.Fatal("access logs contain an identity secret marker")
		}
	}
}
```

- [ ] **Step 2: Add expiry and rate-limit integration tests**

Append to `internal/httpserver/identity_integration_test.go`:

```go
func TestPairingExpiryIntegration(t *testing.T) {
	t.Run("pending request", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Approver")
		status, _, body := h.request(http.MethodPost, "/v1/pairing-requests", "", h.nextKey(), map[string]any{
			"proposed_name": "Expiring", "platform": "linux", "requested_scope": "connector",
		})
		if status != http.StatusCreated {
			t.Fatalf("pairing create status = %d", status)
		}
		var pairing identity.PairingCreateResponse
		if err := json.Unmarshal(body, &pairing); err != nil {
			t.Fatalf("decode pairing: %v", err)
		}
		h.clock.Advance(identity.PairingLifetime + time.Second)
		fullToken := credential(workspace, "full")
		status, _, _ = h.request(http.MethodGet, "/v1/pairing-requests/"+pairing.PairingID, fullToken, "", nil)
		if status != http.StatusGone {
			t.Fatalf("expired details status = %d", status)
		}
		status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/approve", fullToken, h.nextKey(), map[string]any{})
		if status != http.StatusGone {
			t.Fatalf("expired approval status = %d", status)
		}
		status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/claim", "", "", map[string]any{"claim_secret": pairing.ClaimSecret})
		if status != http.StatusGone {
			t.Fatalf("expired claim status = %d", status)
		}
	})

	t.Run("approved details expire before private claim", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Approver")
		fullToken := credential(workspace, "full")
		status, _, body := h.request(http.MethodPost, "/v1/pairing-requests", "", h.nextKey(), map[string]any{
			"proposed_name": "Approved Expiry", "platform": "linux", "requested_scope": "connector",
		})
		if status != http.StatusCreated {
			t.Fatalf("pairing create status = %d", status)
		}
		var pairing identity.PairingCreateResponse
		if err := json.Unmarshal(body, &pairing); err != nil {
			t.Fatalf("decode pairing: %v", err)
		}
		h.clock.Advance(4 * time.Minute)
		status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/approve", fullToken, h.nextKey(), map[string]any{})
		if status != http.StatusOK {
			t.Fatalf("approval status = %d", status)
		}
		h.clock.Advance(time.Minute + time.Second)
		status, _, _ = h.request(http.MethodGet, "/v1/pairing-requests/"+pairing.PairingID, fullToken, "", nil)
		if status != http.StatusGone {
			t.Fatalf("approved expired ID details status = %d", status)
		}
		status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/lookup", fullToken, "", map[string]any{"short_code": pairing.ShortCode})
		if status != http.StatusGone {
			t.Fatalf("approved expired short-code details status = %d", status)
		}
		status, _, claimBody := h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/claim", "", "", map[string]any{"claim_secret": pairing.ClaimSecret})
		if status != http.StatusOK || len(claimBody) == 0 {
			t.Fatalf("private claim status/body bytes = %d/%d", status, len(claimBody))
		}
	})
}

func rateLimitSubjectHash(scope, subject string) []byte {
	digest := sha256.Sum256([]byte("mcpaste-rate-v1\x00" + scope + "\x00" + subject))
	return digest[:]
}

func rateLimitCount(t *testing.T, h *integrationHarness, scope, subject string) int {
	t.Helper()
	var count int
	if err := h.pool.QueryRow(context.Background(), `
select request_count
from rate_limit_buckets
where scope = $1 and subject_hash = $2`, scope, rateLimitSubjectHash(scope, subject)).Scan(&count); err != nil {
		t.Fatalf("inspect rate-limit count for scope %q: %v", scope, err)
	}
	return count
}

func rateLimitTotals(t *testing.T, h *integrationHarness) (int, int) {
	t.Helper()
	var rows int
	var requests int
	if err := h.pool.QueryRow(context.Background(), `
select count(*), coalesce(sum(request_count), 0)
from rate_limit_buckets`).Scan(&rows, &requests); err != nil {
		t.Fatalf("inspect rate-limit totals: %v", err)
	}
	return rows, requests
}

func assertFixedRateLimit(
	t *testing.T,
	h *integrationHarness,
	scope string,
	subject string,
	limit int,
	window time.Duration,
	request func() (int, http.Header, []byte),
) {
	t.Helper()
	now := h.clock.Now()
	resetIn := 1250 * time.Millisecond
	windowStartedAt := now.Add(-window).Add(resetIn)
	subjectHash := rateLimitSubjectHash(scope, subject)
	if _, err := h.pool.Exec(context.Background(), `
insert into rate_limit_buckets(scope, subject_hash, window_started_at, request_count, expires_at)
values ($1, $2, $3, $4, $5)`,
		scope, subjectHash, windowStartedAt, limit, now.Add(identity.RateLimitRetention),
	); err != nil {
		t.Fatalf("seed fixed rate limit: %v", err)
	}
	status, headers, _ := request()
	if status != http.StatusTooManyRequests || headers.Get("Retry-After") != "2" {
		t.Fatalf("rate-limit status/Retry-After = %d/%q", status, headers.Get("Retry-After"))
	}
	var requestCount int
	var storedWindow time.Time
	if err := h.pool.QueryRow(context.Background(), `
select request_count, window_started_at
from rate_limit_buckets
where scope = $1 and subject_hash = $2`, scope, subjectHash).Scan(&requestCount, &storedWindow); err != nil {
		t.Fatalf("inspect fixed rate limit: %v", err)
	}
	if requestCount != limit+1 || !storedWindow.Equal(windowStartedAt) {
		t.Fatalf("rate-limit count/window metadata = %d/%v", requestCount, storedWindow.Equal(windowStartedAt))
	}
}

func corruptRecoveryVerifier(t *testing.T, h *integrationHarness, workspaceID string) {
	t.Helper()
	if _, err := h.pool.Exec(context.Background(), `
update recovery_verifiers
set verifier = decode(repeat('ff', 32), 'hex')
where workspace_id = $1::uuid`, workspaceID); err != nil {
		t.Fatalf("corrupt recovery verifier: %v", err)
	}
}

func TestFixedRateLimitPoliciesIntegration(t *testing.T) {
	t.Run("workspace create 5 per hour by IP", func(t *testing.T) {
		h := newIntegrationHarness(t)
		assertFixedRateLimit(t, h, "workspace.create", "ip:192.0.2.20", 5, time.Hour, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/workspaces", "", h.nextKey(), map[string]any{"device_name": "Limited Mac", "platform": "macos"})
		})
	})

	t.Run("pairing create 10 per 10 minutes by IP", func(t *testing.T) {
		h := newIntegrationHarness(t)
		assertFixedRateLimit(t, h, "pairing.create", "ip:192.0.2.20", 10, 10*time.Minute, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/pairing-requests", "", h.nextKey(), map[string]any{
				"proposed_name": "Limited Pair", "platform": "linux", "requested_scope": "connector",
			})
		})
	})

	t.Run("lookup 30 per 5 minutes by workspace device", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Lookup Mac")
		subject := workspace.WorkspaceID + ":" + workspace.Device.ID
		assertFixedRateLimit(t, h, "pairing.lookup", subject, 30, 5*time.Minute, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/pairing-requests/lookup", credential(workspace, "full"), "", map[string]any{"short_code": "23456789"})
		})
	})

	t.Run("claim 10 per 5 minutes by IP before claim parsing", func(t *testing.T) {
		h := newIntegrationHarness(t)
		pairingID := "00000000-0000-4000-8000-000000000341"
		assertFixedRateLimit(t, h, "pairing.claim.ip", "ip:192.0.2.20", 10, 5*time.Minute, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/pairing-requests/"+pairingID+"/claim", "", "", map[string]any{"claim_secret": "deliberately-invalid"})
		})
	})

	t.Run("claim 10 per 5 minutes by pairing ID before claim parsing", func(t *testing.T) {
		h := newIntegrationHarness(t)
		pairingID := "00000000-0000-4000-8000-000000000342"
		assertFixedRateLimit(t, h, "pairing.claim.id", pairingID, 10, 5*time.Minute, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/pairing-requests/"+pairingID+"/claim", "", "", map[string]any{"claim_secret": "deliberately-invalid"})
		})
	})

	t.Run("recovery 5 per 30 minutes by IP before Argon2id", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Recovery IP Mac")
		corruptRecoveryVerifier(t, h, workspace.WorkspaceID)
		assertFixedRateLimit(t, h, "recovery.ip", "ip:192.0.2.20", 5, 30*time.Minute, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/recoveries", "", h.nextKey(), map[string]any{
				"recovery_code": workspace.RecoveryCode, "device_name": "Blocked Recovery", "platform": "macos",
			})
		})
	})

	t.Run("recovery 5 per 30 minutes by locator before Argon2id", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Recovery Locator Mac")
		workspaceID, locator, err := secure.RecoveryLocator(workspace.RecoveryCode)
		if err != nil {
			t.Fatal("parse generated recovery locator")
		}
		corruptRecoveryVerifier(t, h, workspace.WorkspaceID)
		assertFixedRateLimit(t, h, "recovery.locator", workspaceID+":"+locator, 5, 30*time.Minute, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/recoveries", "", h.nextKey(), map[string]any{
				"recovery_code": workspace.RecoveryCode, "device_name": "Blocked Recovery", "platform": "macos",
			})
		})
	})
}

func TestIdempotentMutationReplayDoesNotConsumeQuotaIntegration(t *testing.T) {
	t.Run("workspace creation replay", func(t *testing.T) {
		h := newIntegrationHarness(t)
		key := h.nextKey()
		input := map[string]any{"device_name": "Replay Workspace", "platform": "macos"}
		status, _, firstBody := h.request(http.MethodPost, "/v1/workspaces", "", key, input)
		if status != http.StatusCreated {
			t.Fatalf("initial workspace status = %d", status)
		}
		before := rateLimitCount(t, h, "workspace.create", "ip:192.0.2.20")
		status, _, replayBody := h.request(http.MethodPost, "/v1/workspaces", "", key, input)
		if status != http.StatusCreated || !bytes.Equal(firstBody, replayBody) {
			t.Fatal("workspace replay status or body differs")
		}
		after := rateLimitCount(t, h, "workspace.create", "ip:192.0.2.20")
		if before != 1 || after != before {
			t.Fatalf("workspace quota before/after replay = %d/%d", before, after)
		}
	})

	t.Run("pairing creation replay", func(t *testing.T) {
		h := newIntegrationHarness(t)
		key := h.nextKey()
		input := map[string]any{
			"proposed_name": "Replay Pairing", "platform": "linux", "requested_scope": "connector",
		}
		status, _, firstBody := h.request(http.MethodPost, "/v1/pairing-requests", "", key, input)
		if status != http.StatusCreated {
			t.Fatalf("initial pairing status = %d", status)
		}
		before := rateLimitCount(t, h, "pairing.create", "ip:192.0.2.20")
		status, _, replayBody := h.request(http.MethodPost, "/v1/pairing-requests", "", key, input)
		if status != http.StatusCreated || !bytes.Equal(firstBody, replayBody) {
			t.Fatal("pairing replay status or body differs")
		}
		after := rateLimitCount(t, h, "pairing.create", "ip:192.0.2.20")
		if before != 1 || after != before {
			t.Fatalf("pairing quota before/after replay = %d/%d", before, after)
		}
	})

	t.Run("approval replay remains quota free", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Approval Replay Mac")
		status, _, pairingBody := h.request(http.MethodPost, "/v1/pairing-requests", "", h.nextKey(), map[string]any{
			"proposed_name": "Approval Replay Joiner", "platform": "linux", "requested_scope": "connector",
		})
		if status != http.StatusCreated {
			t.Fatalf("pairing status = %d", status)
		}
		var pairing identity.PairingCreateResponse
		if err := json.Unmarshal(pairingBody, &pairing); err != nil {
			t.Fatalf("decode pairing response: %v", err)
		}
		var devicesBefore int
		if err := h.pool.QueryRow(context.Background(), `
select count(*) from devices where workspace_id = $1::uuid`, workspace.WorkspaceID).Scan(&devicesBefore); err != nil {
			t.Fatalf("count devices before approval: %v", err)
		}
		rowsBefore, requestsBefore := rateLimitTotals(t, h)
		key := h.nextKey()
		path := "/v1/pairing-requests/" + pairing.PairingID + "/approve"
		status, _, firstBody := h.request(http.MethodPost, path, credential(workspace, "full"), key, map[string]any{})
		if status != http.StatusOK {
			t.Fatalf("initial approval status = %d", status)
		}
		status, _, replayBody := h.request(http.MethodPost, path, credential(workspace, "full"), key, map[string]any{})
		if status != http.StatusOK || !bytes.Equal(firstBody, replayBody) {
			t.Fatal("approval replay status or body differs")
		}
		rowsAfter, requestsAfter := rateLimitTotals(t, h)
		if rowsAfter != rowsBefore || requestsAfter != requestsBefore {
			t.Fatalf("approval rate rows/requests before-after = %d/%d-%d/%d", rowsBefore, requestsBefore, rowsAfter, requestsAfter)
		}
		var devicesAfter int
		if err := h.pool.QueryRow(context.Background(), `
select count(*) from devices where workspace_id = $1::uuid`, workspace.WorkspaceID).Scan(&devicesAfter); err != nil {
			t.Fatalf("count devices after approval replay: %v", err)
		}
		if devicesAfter != devicesBefore+1 {
			t.Fatalf("devices before/after approval replay = %d/%d", devicesBefore, devicesAfter)
		}
	})

	t.Run("recovery replay", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Recovery Replay Mac")
		workspaceID, locator, err := secure.RecoveryLocator(workspace.RecoveryCode)
		if err != nil {
			t.Fatal("parse generated recovery locator")
		}
		key := h.nextKey()
		input := map[string]any{
			"recovery_code": workspace.RecoveryCode, "device_name": "Recovery Replay Joiner", "platform": "macos",
		}
		status, _, firstBody := h.request(http.MethodPost, "/v1/recoveries", "", key, input)
		if status != http.StatusCreated {
			t.Fatalf("initial recovery status = %d", status)
		}
		ipBefore := rateLimitCount(t, h, "recovery.ip", "ip:192.0.2.20")
		locatorBefore := rateLimitCount(t, h, "recovery.locator", workspaceID+":"+locator)
		status, _, replayBody := h.request(http.MethodPost, "/v1/recoveries", "", key, input)
		if status != http.StatusCreated || !bytes.Equal(firstBody, replayBody) {
			t.Fatal("recovery replay status or body differs")
		}
		ipAfter := rateLimitCount(t, h, "recovery.ip", "ip:192.0.2.20")
		locatorAfter := rateLimitCount(t, h, "recovery.locator", workspaceID+":"+locator)
		if ipBefore != 1 || locatorBefore != 1 || ipAfter != ipBefore || locatorAfter != locatorBefore {
			t.Fatalf("recovery IP/locator quota before-after = %d/%d-%d/%d", ipBefore, locatorBefore, ipAfter, locatorAfter)
		}
	})
}

func TestDatabaseBackedReadinessIntegration(t *testing.T) {
	pool := testdb.New(t)
	handler := NewHandler(func(ctx context.Context) error { return database.Ready(ctx, pool) })
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("ready status = %d", ready.Code)
	}
	pool.Close()
	unavailable := httptest.NewRecorder()
	handler.ServeHTTP(unavailable, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if unavailable.Code != http.StatusServiceUnavailable || unavailable.Body.String() != "{\"status\":\"unavailable\"}\n" {
		t.Fatalf("closed pool status/body = %d/%q", unavailable.Code, unavailable.Body.String())
	}
}
```

- [ ] **Step 3: Run the already-defined integration tests**

```bash
gofmt -w internal/httpserver/identity_integration_test.go
export MCPASTE_TEST_DATABASE_URL='postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable'
go test -race ./internal/httpserver -run 'TestIdentityLifecycle|TestPairingExpiry|TestFixedRateLimitPolicies|TestIdempotentMutationReplay|TestDatabaseBacked' -v
```

Expected: PASS. The replay test proves workspace creation, pairing creation, and both recovery subjects retain request count 1 across byte-identical replay; approval replay adds neither a rate row nor a request count and creates exactly one joining device. These assertions fail if completed-response preflight moves below rate limiting. If any command fails, stop execution and revise this plan with a complete, explicit red/green step and exact affected file content before changing production code; do not improvise an unspecified fix.

- [ ] **Step 4: Strengthen Foundation access-log markers**

Replace `TestNewAccessLogMiddlewareLogsMetadataOnly` in `internal/httpserver/logging_test.go` completely with:

```go
func TestNewAccessLogMiddlewareLogsMetadataOnly(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	next := http.NewServeMux()
	next.HandleFunc("POST /v1/example/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	body := `{"short_code":"pairing-short-code-marker","recovery_code":"recovery-code-secret-marker","qr_payload":"qr-payload-secret-marker","body":"body-secret"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/example/path-secret?token=query-secret", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer header-secret")
	request.Header.Set("Idempotency-Key", "idempotency-secret-marker")
	request.Header.Set("X-Forwarded-For", "forwarded-for-secret-marker")
	request.Header.Set("Cookie", "pairing-claim-secret-marker")
	response := httptest.NewRecorder()

	NewAccessLogMiddleware(logger)(next).ServeHTTP(response, request)

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal access log: %v", err)
	}
	if got := entry["method"]; got != http.MethodPost {
		t.Fatalf("method = %v, want %q", got, http.MethodPost)
	}
	if got := entry["path"]; got != "POST /v1/example/{id}" {
		t.Fatalf("path = %v, want %q", got, "POST /v1/example/{id}")
	}
	if got := entry["status"]; got != float64(http.StatusNoContent) {
		t.Fatalf("status = %v, want %d", got, http.StatusNoContent)
	}

	output := logs.String()
	markers := []string{
		"path-secret", "query-secret", "body-secret", "header-secret", "Authorization",
		"idempotency-secret-marker", "pairing-claim-secret-marker", "pairing-short-code-marker",
		"recovery-code-secret-marker", "qr-payload-secret-marker", "forwarded-for-secret-marker",
	}
	for index, marker := range markers {
		if strings.Contains(output, marker) {
			t.Fatalf("access log contains secret marker index %d", index)
		}
	}
}
```

Replace `TestNewRecoveryMiddlewareRecoversWithoutLoggingSecrets` in the same file completely with:

```go
func TestNewRecoveryMiddlewareRecoversWithoutLoggingSecrets(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	next := http.NewServeMux()
	next.HandleFunc("POST /v1/example/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Unsafe-Marker", "response-header-secret")
		_, _ = w.Write([]byte("partial-response-secret"))
		panic("panic-secret")
	})

	body := `{"short_code":"pairing-short-code-marker","recovery_code":"recovery-code-secret-marker","qr_payload":"qr-payload-secret-marker","body":"body-secret"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/example/path-secret?token=query-secret", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer header-secret")
	request.Header.Set("Idempotency-Key", "idempotency-secret-marker")
	request.Header.Set("X-Forwarded-For", "forwarded-for-secret-marker")
	request.Header.Set("Cookie", "pairing-claim-secret-marker")
	response := httptest.NewRecorder()

	NewRecoveryMiddleware(logger)(next).ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if body := response.Body.String(); body != "{\"error\":{\"code\":\"internal_error\"}}\n" {
		t.Fatalf("response body bytes = %d", len(body))
	}
	if response.Header().Get("X-Unsafe-Marker") != "" {
		t.Fatal("panic response retained buffered handler header")
	}

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal recovery log: %v", err)
	}
	if got := entry["msg"]; got != "http panic recovered" {
		t.Fatalf("message = %v, want %q", got, "http panic recovered")
	}
	if got := entry["method"]; got != http.MethodPost {
		t.Fatalf("method = %v, want %q", got, http.MethodPost)
	}
	if got := entry["path"]; got != "POST /v1/example/{id}" {
		t.Fatalf("path = %v, want %q", got, "POST /v1/example/{id}")
	}

	markers := []string{
		"panic-secret", "path-secret", "query-secret", "body-secret", "header-secret", "Authorization",
		"idempotency-secret-marker", "pairing-claim-secret-marker", "pairing-short-code-marker",
		"recovery-code-secret-marker", "qr-payload-secret-marker", "forwarded-for-secret-marker",
		"partial-response-secret", "response-header-secret",
	}
	for index, marker := range markers {
		if strings.Contains(response.Body.String(), marker) || strings.Contains(response.Header().Get("X-Unsafe-Marker"), marker) || strings.Contains(logs.String(), marker) {
			t.Fatalf("recovery boundary contains secret marker index %d", index)
		}
	}
}

func TestNewRecoveryMiddlewarePreservesFoundationNonV1Response(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("foundation-panic-secret")
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/foundation-panic", nil)

	NewRecoveryMiddleware(logger)(next).ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError || response.Body.String() != "internal server error\n" {
		t.Fatalf("non-v1 status/body bytes = %d/%d", response.Code, response.Body.Len())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/plain; charset=utf-8" {
		t.Fatalf("non-v1 Content-Type = %q", contentType)
	}
	if strings.Contains(logs.String(), "foundation-panic-secret") {
		t.Fatal("non-v1 recovery log contains panic value")
	}
}
```

Run the strengthened recovery tests before changing the middleware:

```bash
gofmt -w internal/httpserver/logging_test.go
go test ./internal/httpserver -run 'TestNewRecoveryMiddlewareRecoversWithoutLoggingSecrets|TestNewRecoveryMiddlewarePreservesFoundationNonV1Response' -count=1
```

Expected: FAIL because the current Foundation recovery writes plain text for `/v1/` and cannot discard a partial response. The non-v1 subtest continues to pass.

- [ ] **Step 5: Implement canonical `/v1/` panic recovery and prove readiness never leaks PostgreSQL errors**

Replace the import block and `NewRecoveryMiddleware` in `internal/httpserver/logging.go`, and append the buffer methods, with this exact content; leave `NewAccessLogMiddleware`, `safeRoute`, and `statusWriter` unchanged:

```go
import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func NewRecoveryMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1" || strings.HasPrefix(r.URL.Path, "/v1/") {
				serveV1WithRecovery(logger, next, w, r)
				return
			}
			defer func() {
				if recover() != nil {
					logRecoveredPanic(logger, r)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func serveV1WithRecovery(logger *slog.Logger, next http.Handler, w http.ResponseWriter, r *http.Request) {
	buffered := &bufferedResponse{header: make(http.Header), status: http.StatusOK}
	defer func() {
		if recover() != nil {
			logRecoveredPanic(logger, r)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{"code": "internal_error"},
			})
			return
		}
		buffered.flush(w)
	}()
	next.ServeHTTP(buffered, r)
}

func logRecoveredPanic(logger *slog.Logger, r *http.Request) {
	logger.Error("http panic recovered",
		slog.String("method", r.Method),
		slog.String("path", safeRoute(r.Pattern)),
	)
}

type bufferedResponse struct {
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func (w *bufferedResponse) Header() http.Header {
	return w.header
}

func (w *bufferedResponse) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
}

func (w *bufferedResponse) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(data)
}

func (w *bufferedResponse) flush(target http.ResponseWriter) {
	for name, values := range w.header {
		for _, value := range values {
			target.Header().Add(name, value)
		}
	}
	target.WriteHeader(w.status)
	_, _ = target.Write(w.body.Bytes())
}
```

Replace `TestReadyz` in `internal/httpserver/health_test.go` completely with:

```go
func TestReadyz(t *testing.T) {
	tests := []struct {
		name       string
		readiness  ReadinessFunc
		wantStatus int
		wantBody   string
	}{
		{
			name: "ready",
			readiness: func(_ context.Context) error {
				return nil
			},
			wantStatus: http.StatusOK,
			wantBody:   "{\"status\":\"ok\"}\n",
		},
		{
			name: "unavailable",
			readiness: func(_ context.Context) error {
				return errors.New("database-password-secret")
			},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "{\"status\":\"unavailable\"}\n",
		},
		{
			name: "database detail redacted",
			readiness: func(_ context.Context) error {
				return errors.New("postgres-password-secret-marker")
			},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "{\"status\":\"unavailable\"}\n",
		},
	}

	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			response := httptest.NewRecorder()

			NewHandler(item.readiness).ServeHTTP(response, request)

			if response.Code != item.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, item.wantStatus)
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", contentType)
			}
			if body := response.Body.String(); body != item.wantBody {
				t.Fatalf("response body bytes = %d, want %d", len(body), len(item.wantBody))
			}
			for markerIndex, marker := range []string{"database-password-secret", "postgres-password-secret-marker"} {
				if strings.Contains(response.Body.String(), marker) {
					t.Fatalf("response contains readiness marker index %d", markerIndex)
				}
			}
		})
	}
}
```

- [ ] **Step 6: Run complete security and integration coverage**

```bash
gofmt -w internal/httpserver/logging.go internal/httpserver/logging_test.go internal/httpserver/health_test.go
export MCPASTE_TEST_DATABASE_URL='postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable'
go test -race ./internal/httpserver ./internal/identity ./internal/identity/postgres ./internal/secure ./internal/database/migrate
go vet ./internal/httpserver ./internal/identity ./internal/identity/postgres ./internal/secure
```

Expected: PASS. This single gate proves anonymous creation, exact credentials, full/connector pairing, pending/expiry/approval/claim replay, duplicate names, rename/revoke, revoked auth, recovery rotation/preservation, wrong/corrupt and concurrency-bounded Argon2 verification, AES misuse resistance, rate limiting, idempotent retry, isolation, readiness, canonical buffered `/v1/` panic errors, unchanged Foundation non-v1 panic output, and log exclusion.

- [ ] **Step 7: Commit lifecycle acceptance coverage**

```bash
git add internal/httpserver/identity_integration_test.go internal/httpserver/logging.go internal/httpserver/logging_test.go internal/httpserver/health_test.go
git commit -m "test: cover identity lifecycle security"
```

Expected: one lifecycle-security commit containing integration tests plus the reviewed `/v1/` panic boundary. No test prints runtime credentials or recovery/claim values on failure.

## Task 12: Wire the server, cleanup loop, local setup, container, and CI

**Files:**

- Modify: `.env.example`
- Modify: `README.md`
- Modify: `docs/security-and-secrets.md`
- Modify: `cmd/server/main.go`
- Create: `cmd/server/main_test.go`
- Modify: `.dockerignore`
- Modify: `Dockerfile`
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: Replace the safe environment template**

Replace `.env.example` with:

```dotenv
# Copy non-sensitive defaults to .env.local, append one generated local key, then source it.
# Never place paste content, bearer credentials, recovery codes, pairing data, or encryption keys here.

MCPASTE_ENV=development
MCPASTE_HTTP_ADDR=:8080
MCPASTE_LOG_LEVEL=info
MCPASTE_DATABASE_URL=postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable
MCPASTE_ACTIVE_KEY_ID=local-dev-v1
MCPASTE_CLEANUP_INTERVAL=15m
MCPASTE_TRUSTED_PROXY_CIDRS=
```

`MCPASTE_ENCRYPTION_KEYS` is intentionally absent because even a local encryption key must not be committed.

- [ ] **Step 2: Patch README local setup and API boundary exactly**

Apply this patch to `README.md`:

```diff
@@
-Run the Go checks and server with:
+Start PostgreSQL, initialize the ignored mode-0600 local environment once, migrate, and run the server with:
+
 ```sh
+if ! command -v lsof >/dev/null 2>&1; then
+  printf '%s\n' 'lsof is required for the read-only TCP port preflight.' >&2
+  exit 1
+fi
+listener_status=0
+listener_output="$(lsof -nP -iTCP:55439 -sTCP:LISTEN 2>&1)" || listener_status=$?
+if test "$listener_status" -gt 1; then
+  printf '%s\n' 'Unable to inspect TCP port 55439; stop without starting PostgreSQL.' >&2
+  exit 1
+fi
+if test -n "$listener_output"; then
+  printf '%s\n' 'TCP port 55439 is already in use. Stop here and identify its owner manually; do not stop or alter that process or container.' >&2
+  printf '%s\n' "$listener_output" >&2
+  exit 1
+fi
+unset listener_output listener_status
+docker compose up -d --wait --wait-timeout 60 postgres
+if [ ! -f .env.local ]; then
+  umask 077
+  cp .env.example .env.local
+fi
+chmod 600 .env.local
+set -a
+source .env.local
+set +a
+if ! grep -q '^MCPASTE_ENCRYPTION_KEYS=' .env.local; then
+  mcpaste_local_key="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')"
+  printf '\nMCPASTE_ENCRYPTION_KEYS=%s:%s\n' "$MCPASTE_ACTIVE_KEY_ID" "$mcpaste_local_key" >>.env.local
+  unset mcpaste_local_key
+fi
+set -a
+source .env.local
+set +a
+go run ./cmd/migrate up
 go test ./cmd/migrate ./cmd/server ./db/migrations ./internal/config ./internal/database ./internal/database/migrate ./internal/httpserver ./internal/identity ./internal/identity/postgres ./internal/secure ./internal/testdb
 go run ./cmd/server
 ```
+
+The listener check is read-only. If port 55439 is occupied, stop setup and identify the owner; never stop or reconfigure an unrelated process or container. `docker compose up -d --wait --wait-timeout 60 postgres` waits for the defined healthcheck and fails after 60 seconds instead of racing a cold database. On later sessions, run `set -a; source .env.local; set +a`; the bootstrap keeps all existing non-sensitive variables and never replaces an existing `MCPASTE_ENCRYPTION_KEYS` line. Keep every old key ID and key value while the PostgreSQL volume contains encrypted replay rows. For an intentional rotation, add a newly generated key under a new ID, retain all old `id:key` entries in `MCPASTE_ENCRYPTION_KEYS`, and change `MCPASTE_ACTIVE_KEY_ID` to the new ID. Never reuse an old ID with new bytes. If the retained key is lost or a disposable local reset is preferred, run `docker compose down --volumes`, remove `.env.local`, and rerun the bootstrap; the volume deletion is destructive.
+
+`go run ./cmd/migrate status` and `go run ./cmd/migrate verify` must report `applied=1 available=1`. `go run ./cmd/migrate down --steps 1` is destructive local rollback and is never part of application rollback. Stop local PostgreSQL without deleting data with `docker compose down`.
+
+Phase 2 exposes anonymous workspace, pairing, recovery, and full-device administration under `/v1/`. Authorization and idempotency values use headers. Pairing claim and recovery values use JSON request bodies. Never put tokens, pairing codes, claim secrets, recovery codes, or QR payloads in a URL, command history, log, screenshot, issue, or pull request.
```

Expected: existing product boundary, trust model, architecture, and design links remain unchanged.

- [ ] **Step 3: Patch security documentation with the Phase 2 secret boundary**

Apply this patch to `docs/security-and-secrets.md` immediately before `## Pre-commit check`:

```diff
@@
+## Phase 2 identity material
+
+Production receives `MCPASTE_DATABASE_URL`, `MCPASTE_ACTIVE_KEY_ID`, and `MCPASTE_ENCRYPTION_KEYS` through the server process environment populated from `/etc/mcpaste/server.env`. `MCPASTE_ENCRYPTION_KEYS` is a comma-separated keyring of raw URL-base64 AES-256 keys. The active key identifier is stored with each ciphertext; old keys remain in the process keyring only while retained ciphertext still references them. The service never reads an encryption key from a repository file, command argument, request, database row, or client.
+
+Bearer credentials and private pairing claim secrets contain 256 random bits. PostgreSQL stores only domain-separated SHA-256 hashes and non-secret lookup locators. Recovery codes contain 256 random bits and use a workspace UUID plus non-secret locator for indexed lookup; PostgreSQL stores a random salt and Argon2id verifier, never the code. Recovery rotation replaces the verifier in the same transaction that adds the recovered full Mac.
+
+Credential-bearing workspace and recovery responses are encrypted for 24-hour idempotent replay. Approved pairing grants are encrypted for five-minute, byte-identical claim replay. The encrypted rows remain server-sensitive because the running service can decrypt them. Cleanup purges expired replay state and revokes approved devices whose pairing grants expire without a successful claim.
+
+Initialize the ignored local keyring only when it is absent:
+
+```sh
+if [ ! -f .env.local ]; then
+  umask 077
+  cp .env.example .env.local
+fi
+chmod 600 .env.local
+set -a
+source .env.local
+set +a
+if ! grep -q '^MCPASTE_ENCRYPTION_KEYS=' .env.local; then
+  mcpaste_local_key="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')"
+  printf '\nMCPASTE_ENCRYPTION_KEYS=%s:%s\n' "$MCPASTE_ACTIVE_KEY_ID" "$mcpaste_local_key" >>.env.local
+  unset mcpaste_local_key
+fi
+set -a
+source .env.local
+set +a
+```
+
+The file remains mode 0600 and ignored. Later sessions source it instead of generating another key. Never replace key bytes under a retained key ID while the PostgreSQL volume exists: intentionally rotate by adding a new ID/key pair, retaining every old pair needed by ciphertext, and selecting only the new ID for writes. If local encrypted data is disposable, `docker compose down --volumes` plus removal and recreation of `.env.local` resets both sides together. Do not paste a key value into `.env.example`, documentation, tests, shell transcripts, or review comments. Production keys come only from the root-owned server environment file described below.
+
 ## Pre-commit check
```

- [ ] **Step 4: Write the migration-current startup and readiness test first**

Create `cmd/server/main_test.go` with:

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/db/migrations"
	"github.com/1yoouoo/mcpaste/internal/database/migrate"
	"github.com/1yoouoo/mcpaste/internal/httpserver"
	"github.com/1yoouoo/mcpaste/internal/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStartupAndReadinessRequireCurrentSchema(t *testing.T) {
	ctx := context.Background()
	pool := testdb.NewUnmigrated(t)
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := requireCurrentSchema(ctx, pool, available); err == nil || err.Error() != "database schema is not current" {
		t.Fatalf("startup schema error metadata: nil=%v", err == nil)
	}
	handler := httpserver.NewHandler(databaseReadiness(pool, available))
	unmigrated := httptest.NewRecorder()
	handler.ServeHTTP(unmigrated, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if unmigrated.Code != http.StatusServiceUnavailable || unmigrated.Body.String() != "{\"status\":\"unavailable\"}\n" {
		t.Fatalf("unmigrated readiness metadata = %d/%d", unmigrated.Code, unmigrated.Body.Len())
	}
	if err := migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		return migrate.Up(ctx, conn, available)
	}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if err := requireCurrentSchema(ctx, pool, available); err != nil {
		t.Fatalf("current startup schema error = %v", err)
	}
	current := httptest.NewRecorder()
	handler.ServeHTTP(current, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if current.Code != http.StatusOK || current.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("current readiness metadata = %d/%d", current.Code, current.Body.Len())
	}
}

func TestStartupAndReadinessRejectUnknownVersionAndChecksumDrift(t *testing.T) {
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	tests := []struct {
		name      string
		statement string
		argument1 any
		argument2 any
	}{
		{
			name: "unknown applied version",
			statement: `
insert into schema_migrations(version, name, checksum)
values ($1, 'unknown', $2)`,
			argument1: available[len(available)-1].Version + 1,
			argument2: strings.Repeat("0", 64),
		},
		{
			name:      "checksum drift",
			statement: "update schema_migrations set checksum = $1 where version = $2",
			argument1: strings.Repeat("0", 64),
			argument2: available[0].Version,
		},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			ctx := context.Background()
			pool := testdb.New(t)
			if _, err := pool.Exec(ctx, item.statement, item.argument1, item.argument2); err != nil {
				t.Fatal("mutate isolated migration state")
			}

			startupErr := requireCurrentSchema(ctx, pool, available)
			if startupErr == nil || startupErr.Error() != "database schema is not current" {
				t.Fatalf("startup rejection metadata: nil=%v generic=%v", startupErr == nil, startupErr != nil && startupErr.Error() == "database schema is not current")
			}

			readiness := databaseReadiness(pool, available)
			readinessErr := readiness(ctx)
			if readinessErr == nil || readinessErr.Error() != "database unavailable" {
				t.Fatalf("readiness closure metadata: nil=%v generic=%v", readinessErr == nil, readinessErr != nil && readinessErr.Error() == "database unavailable")
			}
			handler := httpserver.NewHandler(readiness)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if response.Code != http.StatusServiceUnavailable || response.Body.String() != "{\"status\":\"unavailable\"}\n" {
				t.Fatalf("readyz rejection metadata = %d/%d", response.Code, response.Body.Len())
			}

			for markerIndex, marker := range []string{
				"postgres://", "schema_migrations", "checksum", strings.Repeat("0", 64),
			} {
				if strings.Contains(startupErr.Error(), marker) || strings.Contains(readinessErr.Error(), marker) || strings.Contains(response.Body.String(), marker) {
					t.Fatalf("migration rejection leaked marker index %d", markerIndex)
				}
			}
		})
	}
}

func TestReadinessDoesNotWaitForMigrationLockAndIsBounded(t *testing.T) {
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	t.Run("migration advisory lock does not block readiness", func(t *testing.T) {
		pool := testdb.New(t)
		lockHeld := make(chan struct{})
		releaseLock := make(chan struct{})
		lockResult := make(chan error, 1)
		go func() {
			lockResult <- migrate.WithLock(context.Background(), pool, func(*pgx.Conn) error {
				close(lockHeld)
				<-releaseLock
				return nil
			})
		}()
		select {
		case <-lockHeld:
		case <-time.After(2 * time.Second):
			t.Fatal("migration advisory lock was not acquired")
		}
		readinessCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if err := databaseReadiness(pool, available)(readinessCtx); err != nil {
			close(releaseLock)
			<-lockResult
			t.Fatal("readiness waited for migration advisory lock")
		}
		close(releaseLock)
		if err := <-lockResult; err != nil {
			t.Fatal("migration lock holder returned an error")
		}
	})

	t.Run("pool starvation is bounded", func(t *testing.T) {
		if databaseReadinessTimeout != 2*time.Second {
			t.Fatalf("readiness timeout = %v", databaseReadinessTimeout)
		}
		pool := testdb.New(t)
		held := make([]*pgxpool.Conn, 0, pool.Config().MaxConns)
		for index := int32(0); index < pool.Config().MaxConns; index++ {
			conn, err := pool.Acquire(context.Background())
			if err != nil {
				t.Fatal("acquire pool-starvation connection")
			}
			held = append(held, conn)
		}
		defer func() {
			for _, conn := range held {
				conn.Release()
			}
		}()
		started := time.Now()
		err := databaseReadinessWithin(pool, available, 50*time.Millisecond)(context.Background())
		if err == nil || err.Error() != "database unavailable" {
			t.Fatal("pool-starved readiness did not fail generically")
		}
		if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
			t.Fatalf("pool-starved readiness exceeded bound: %v", elapsed)
		}
	})
}
```

Run:

```bash
gofmt -w cmd/server/main_test.go
export MCPASTE_TEST_DATABASE_URL='postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable'
go test ./cmd/server -run 'TestStartupAndReadinessRequireCurrentSchema|TestStartupAndReadinessRejectUnknownVersionAndChecksumDrift|TestReadinessDoesNotWaitForMigrationLockAndIsBounded'
```

Expected: FAIL because `requireCurrentSchema`, `databaseReadiness`, and its bounded helper are undefined. Each drift subtest starts from its own migrated schema through `testdb.New(t)`; that helper registers pool close and cascading removal of that isolated schema on the subtest before either mutation occurs. The advisory-lock test will fail if readiness calls `WithLock`, and the saturation test fixes the production timeout at two seconds while using a 50 ms injected bound for a fast resource-starvation proof.

- [ ] **Step 5: Replace the server entry point with migration-current verification and stateful wiring**

Replace `cmd/server/main.go` with:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/1yoouoo/mcpaste/db/migrations"
	"github.com/1yoouoo/mcpaste/internal/config"
	"github.com/1yoouoo/mcpaste/internal/database"
	"github.com/1yoouoo/mcpaste/internal/database/migrate"
	"github.com/1yoouoo/mcpaste/internal/httpserver"
	"github.com/1yoouoo/mcpaste/internal/identity"
	identitypostgres "github.com/1yoouoo/mcpaste/internal/identity/postgres"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mcpaste server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadOS()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		return errors.New("load schema migrations")
	}
	if err := requireCurrentSchema(ctx, pool, available); err != nil {
		return err
	}
	keyring, err := secure.ParseKeyring(cfg.ActiveKeyID, cfg.EncryptionKeys, secure.SystemRandom{})
	if err != nil {
		return errors.New("load encryption keyring")
	}
	service := identity.NewService(identitypostgres.New(pool), keyring, secure.SystemRandom{}, identity.RealClock{})
	application := httpserver.NewApplicationHandler(
		databaseReadiness(pool, available),
		service,
		cfg.TrustedProxyCIDRs,
	)
	handler := httpserver.NewRecoveryMiddleware(logger)(httpserver.NewAccessLogMiddleware(logger)(application))
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go runCleanup(ctx, logger, service, cfg.CleanupInterval)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("server listening", slog.String("address", cfg.HTTPAddr), slog.String("environment", string(cfg.Environment)))
		serverErrors <- server.ListenAndServe()
	}()
	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func requireCurrentSchema(ctx context.Context, pool *pgxpool.Pool, available []migrate.Migration) error {
	err := migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		_, err := migrate.RequireCurrent(ctx, conn, available)
		return err
	})
	if err != nil {
		return errors.New("database schema is not current")
	}
	return nil
}

const databaseReadinessTimeout = 2 * time.Second

func databaseReadiness(pool *pgxpool.Pool, available []migrate.Migration) httpserver.ReadinessFunc {
	return databaseReadinessWithin(pool, available, databaseReadinessTimeout)
}

func databaseReadinessWithin(pool *pgxpool.Pool, available []migrate.Migration, timeout time.Duration) httpserver.ReadinessFunc {
	return func(ctx context.Context) error {
		checkCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := database.Ready(checkCtx, pool); err != nil {
			return errors.New("database unavailable")
		}
		if _, err := migrate.CheckCurrent(checkCtx, pool, available); err != nil {
			return errors.New("database unavailable")
		}
		return nil
	}
}

func runCleanup(ctx context.Context, logger *slog.Logger, service *identity.Service, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := service.Cleanup(ctx)
			if err != nil {
				logger.Error("identity cleanup failed")
				continue
			}
			logger.Info("identity cleanup complete",
				slog.Int64("revoked_devices", result.RevokedDevices),
				slog.Int64("pairing_rows", result.PairingRows),
				slog.Int64("idempotency_rows", result.IdempotencyRows),
				slog.Int64("event_rows", result.EventRows),
				slog.Int64("rate_limit_rows", result.RateLimitRows),
			)
		}
	}
}
```

The cleanup logger deliberately omits the error value. It records counts only.

- [ ] **Step 6: Build and smoke-test the stateful server locally**

```bash
/bin/bash <<'BASH'
set -euo pipefail
cd /Users/blanc/Documents/Project/mcpaste
acceptance_port=18082
if ! command -v lsof >/dev/null 2>&1; then
  printf '%s\n' 'Port preflight tool is unavailable.' >&2
  exit 1
fi
listener_status=0
listener_output="$(lsof -nP -iTCP:"$acceptance_port" -sTCP:LISTEN 2>/dev/null)" || listener_status=$?
if [[ "$listener_status" -gt 1 ]]; then
  printf '%s\n' 'Acceptance port preflight failed.' >&2
  exit 1
fi
if [[ -n "$listener_output" ]]; then
  printf '%s\n' 'Dedicated acceptance port is already in use.' >&2
  exit 1
fi
unset listener_output listener_status

if [[ ! -f .env.local ]]; then
  umask 077
  cp .env.example .env.local
fi
chmod 600 .env.local
set -a
source .env.local
set +a
if ! grep -q '^MCPASTE_ENCRYPTION_KEYS=' .env.local; then
  mcpaste_local_key="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')"
  printf '\nMCPASTE_ENCRYPTION_KEYS=%s:%s\n' "$MCPASTE_ACTIVE_KEY_ID" "$mcpaste_local_key" >>.env.local
  unset mcpaste_local_key
fi
set -a
source .env.local
set +a
export MCPASTE_TEST_DATABASE_URL="$MCPASTE_DATABASE_URL"
export MCPASTE_HTTP_ADDR="127.0.0.1:${acceptance_port}"

mcpaste_phase2_pid=''
mcpaste_phase2_log="$(mktemp)"
cleanup() {
  cleanup_status=$?
  trap - EXIT INT TERM
  if [[ -n "$mcpaste_phase2_pid" ]]; then
    if kill -0 "$mcpaste_phase2_pid" 2>/dev/null; then
      kill "$mcpaste_phase2_pid" 2>/dev/null || true
    fi
    wait "$mcpaste_phase2_pid" 2>/dev/null || true
  fi
  rm -f "$mcpaste_phase2_log"
  exit "$cleanup_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker compose up -d --wait --wait-timeout 60 postgres
docker compose exec -T postgres pg_isready -U mcpaste -d mcpaste >/dev/null
go run ./cmd/migrate up
gofmt -w cmd/server/main.go
go test -race ./cmd/server -run 'TestStartupAndReadinessRequireCurrentSchema|TestStartupAndReadinessRejectUnknownVersionAndChecksumDrift|TestReadinessDoesNotWaitForMigrationLockAndIsBounded' -count=1
go test -race ./cmd/server
go build -o /tmp/mcpaste-server ./cmd/server
/tmp/mcpaste-server >"$mcpaste_phase2_log" 2>&1 &
mcpaste_phase2_pid=$!
phase2_server_owns_listener() {
  if [[ -z "$mcpaste_phase2_pid" ]] || ! kill -0 "$mcpaste_phase2_pid" 2>/dev/null; then
	return 1
  fi
  owner_status=0
  owner_output="$(lsof -nP -a -p "$mcpaste_phase2_pid" -iTCP:"$acceptance_port" -sTCP:LISTEN -t 2>/dev/null)" || owner_status=$?
  [[ "$owner_status" -eq 0 && "$owner_output" == "$mcpaste_phase2_pid" ]]
}
require_phase2_server() {
  if ! phase2_server_owns_listener; then
    printf '%s\n' 'Acceptance server is unavailable.' >&2
    return 1
  fi
}
listener_owned=0
for attempt in $(seq 1 20); do
  if [[ -z "$mcpaste_phase2_pid" ]] || ! kill -0 "$mcpaste_phase2_pid" 2>/dev/null; then
    printf '%s\n' 'Acceptance server is unavailable.' >&2
    exit 1
  fi
  if phase2_server_owns_listener; then
    listener_owned=1
    break
  fi
  sleep 1
done
if [[ "$listener_owned" -ne 1 ]]; then
  printf '%s\n' 'Acceptance server is unavailable.' >&2
  exit 1
fi
ready_body=''
ready_reached=0
for attempt in $(seq 1 20); do
  require_phase2_server
  if ready_body="$(curl --fail --silent "http://127.0.0.1:${acceptance_port}/readyz" 2>/dev/null)"; then
    ready_reached=1
    break
  fi
  sleep 1
done
test "$ready_reached" = "1"
test "$ready_body" = '{"status":"ok"}'
require_phase2_server
kill "$mcpaste_phase2_pid"
if ! wait "$mcpaste_phase2_pid"; then
  printf '%s\n' 'Acceptance server did not stop cleanly.' >&2
  exit 1
fi
mcpaste_phase2_pid=''
BASH
```

Expected: Compose waits at most 60 seconds for the PostgreSQL healthcheck and the post-wait assertion passes. Focused startup/readiness tests pass for unmigrated, current, unknown-version, checksum-drift, advisory-lock-held, and saturated-pool states. Startup takes the advisory lock once; readiness uses only ping plus the read-only `CheckCurrent` query under one two-second bound. The fail-closed preflight protects loopback port 18082; startup waits at most 20 seconds for `lsof` to prove the exact spawned PID owns that LISTEN socket, and every readiness curl repeats both the ownership and `kill -0` checks. Drift cases expose only generic errors plus the exact `503 {"status":"unavailable"}` body. The process terminates and is waited, and the trap removes the temporary log while preserving any earlier failure status. Keep `.env.local` for later development sessions; it is ignored, mode 0600, and contains the retained key required by the persistent local PostgreSQL volume, but Task 13 final acceptance never sources it.

- [ ] **Step 7: Keep the Docker context default-deny and allow migration inputs**

Replace `.dockerignore` with:

```dockerignore
**
!go.mod
!go.sum
!cmd/
!cmd/**
!internal/
!internal/**
!db/
!db/**
```

- [ ] **Step 8: Build separate server and migration executables**

Replace `Dockerfile` with:

```dockerfile
FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
ENV GOFLAGS=-mod=readonly

COPY cmd ./cmd
COPY internal ./internal
COPY db ./db

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mcpaste-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mcpaste-migrate ./cmd/migrate

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/mcpaste-server /mcpaste-server
COPY --from=build /out/mcpaste-migrate /mcpaste-migrate

USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/mcpaste-server"]
```

- [ ] **Step 9: Replace CI with PostgreSQL-backed Go checks while preserving hardening**

Replace `.github/workflows/ci.yml` with:

```yaml
name: CI

on:
  pull_request:
  push:
    branches:
      - main

permissions:
  contents: read

concurrency:
  group: ci-${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  go:
    name: Go checks
    runs-on: ubuntu-24.04
    timeout-minutes: 20
    services:
      postgres:
        image: postgres:18.4-alpine3.24@sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15
        env:
          POSTGRES_DB: mcpaste
          POSTGRES_USER: mcpaste
          POSTGRES_PASSWORD: mcpaste-ci-only-not-production
        ports:
          - 55439:5432
        options: >-
          --health-cmd "pg_isready -U mcpaste -d mcpaste"
          --health-interval 2s
          --health-timeout 3s
          --health-retries 20
    env:
      MCPASTE_DATABASE_URL: postgres://mcpaste:mcpaste-ci-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable
      MCPASTE_TEST_DATABASE_URL: postgres://mcpaste:mcpaste-ci-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable
    steps:
      - name: Check out repository
        uses: actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10 # v6.0.3
        with:
          persist-credentials: false

      - name: Set up Go
        uses: actions/setup-go@4b73464bb391d4059bd26b0524d20df3927bd417 # v6.3.0
        with:
          go-version-file: .go-version
          cache: false

      - name: Verify module graph
        run: |
          go mod tidy -diff
          go mod verify

      - name: Verify migrations
        run: |
          GOFLAGS=-mod=readonly go run ./cmd/migrate up
          GOFLAGS=-mod=readonly go run ./cmd/migrate verify

      - name: Check formatting
        run: test -z "$(find cmd internal db -type f -name '*.go' -exec gofmt -l {} +)"

      - name: Vet
        run: GOFLAGS=-mod=readonly go vet ./cmd/migrate ./cmd/server ./db/migrations ./internal/config ./internal/database ./internal/database/migrate ./internal/httpserver ./internal/identity ./internal/identity/postgres ./internal/secure ./internal/testdb

      - name: Test with race detector and PostgreSQL
        run: GOFLAGS=-mod=readonly go test -race -coverprofile=coverage.out ./cmd/migrate ./cmd/server ./db/migrations ./internal/config ./internal/database ./internal/database/migrate ./internal/httpserver ./internal/identity ./internal/identity/postgres ./internal/secure ./internal/testdb

      - name: Build server and migration command
        run: |
          GOFLAGS=-mod=readonly go build -o /tmp/mcpaste-server ./cmd/server
          GOFLAGS=-mod=readonly go build -o /tmp/mcpaste-migrate ./cmd/migrate

      - name: Check known Go vulnerabilities
        run: GOFLAGS=-mod=readonly go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./cmd/migrate ./cmd/server ./db/migrations ./internal/config ./internal/database ./internal/database/migrate ./internal/httpserver ./internal/identity ./internal/identity/postgres ./internal/secure ./internal/testdb

  container:
    name: Container build
    runs-on: ubuntu-24.04
    timeout-minutes: 15
    steps:
      - name: Check out repository
        uses: actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10 # v6.0.3
        with:
          persist-credentials: false

      - name: Build server image
        run: docker build -t mcpaste-server:ci .

      - name: Inspect non-root default
        run: test "$(docker image inspect mcpaste-server:ci --format '{{.Config.User}}')" = "65532:65532"

  secrets:
    name: Secret scan
    runs-on: ubuntu-24.04
    timeout-minutes: 15
    steps:
      - name: Check out full history
        uses: actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10 # v6.0.3
        with:
          fetch-depth: 0
          persist-credentials: false

      - name: Install Gitleaks
        run: |
          curl --fail --location --silent --show-error \
            --output /tmp/gitleaks.tar.gz \
            https://github.com/gitleaks/gitleaks/releases/download/v8.24.3/gitleaks_8.24.3_linux_x64.tar.gz
          echo "9991e0b2903da4c8f6122b5c3186448b927a5da4deef1fe45271c3793f4ee29c  /tmp/gitleaks.tar.gz" | sha256sum --check
          tar -xzf /tmp/gitleaks.tar.gz -C /tmp gitleaks

      - name: Reject obvious secret patterns without disclosure
        run: |
          if rg --quiet --hidden \
            --glob '!.git/**' \
            --glob '!.github/workflows/ci.yml' \
            --glob '!docs/security-and-secrets.md' \
            --glob '!docs/superpowers/plans/**' \
            '(sk-[A-Za-z0-9]{12,}|ghp_[A-Za-z0-9]{12,}|AKIA[0-9A-Z]{16}|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY)' .
          then
            printf '%s\n' 'Potential secret pattern detected.' >&2
            exit 1
          else
            secret_pattern_status=$?
            if [ "$secret_pattern_status" -ne 1 ]; then
              printf '%s\n' 'Secret-pattern scan failed.' >&2
              exit 1
            fi
          fi

      - name: Scan repository
        run: |
          if ! /tmp/gitleaks detect --source . --redact >/tmp/gitleaks-result 2>&1; then
            printf '%s\n' 'Gitleaks detected a secret or failed to scan.' >&2
            exit 1
          fi
```

- [ ] **Step 10: Reproduce CI and container checks locally**

```bash
/bin/bash <<'BASH'
set -euo pipefail
cd /Users/blanc/Documents/Project/mcpaste
set -a
source .env.local
set +a
export MCPASTE_TEST_DATABASE_URL="$MCPASTE_DATABASE_URL"
docker compose up -d --wait --wait-timeout 60 postgres
docker compose exec -T postgres pg_isready -U mcpaste -d mcpaste >/dev/null
test -z "$(find cmd internal db -type f -name '*.go' -exec gofmt -l {} +)"
go mod tidy -diff
go mod verify
GOFLAGS=-mod=readonly go run ./cmd/migrate up
GOFLAGS=-mod=readonly go run ./cmd/migrate verify
GOFLAGS=-mod=readonly go vet ./cmd/migrate ./cmd/server ./db/migrations ./internal/config ./internal/database ./internal/database/migrate ./internal/httpserver ./internal/identity ./internal/identity/postgres ./internal/secure ./internal/testdb
GOFLAGS=-mod=readonly go test -race -coverprofile=coverage.out ./cmd/migrate ./cmd/server ./db/migrations ./internal/config ./internal/database ./internal/database/migrate ./internal/httpserver ./internal/identity ./internal/identity/postgres ./internal/secure ./internal/testdb
GOFLAGS=-mod=readonly go build -o /tmp/mcpaste-server ./cmd/server
GOFLAGS=-mod=readonly go build -o /tmp/mcpaste-migrate ./cmd/migrate
GOFLAGS=-mod=readonly go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./cmd/migrate ./cmd/server ./db/migrations ./internal/config ./internal/database ./internal/database/migrate ./internal/httpserver ./internal/identity ./internal/identity/postgres ./internal/secure ./internal/testdb
docker build -t mcpaste-server:phase2 .
docker image inspect mcpaste-server:phase2 --format '{{.Config.User}}'
BASH
```

Expected: Compose waits at most 60 seconds for the defined PostgreSQL healthcheck and the post-wait `pg_isready` assertion passes. Every command exits 0, `go mod tidy -diff` emits no diff, `go mod verify` reports verified modules, every compile/test/vet/build command runs with readonly module mode, Govulncheck reports no reachable known vulnerability, the image builds both binaries, and image user is `65532:65532`.

- [ ] **Step 11: Verify CI hardening and no deployment**

```bash
ruby -e 'require "yaml"; YAML.parse_file(".github/workflows/ci.yml"); puts "valid yaml"'
rg -n 'permissions:|contents: read|persist-credentials: false|sha256sum --check|go mod tidy -diff|go mod verify|GOFLAGS=-mod=readonly|rg --quiet|services:|postgres:18.4-alpine3.24@sha256:' .github/workflows/ci.yml
rg -n '(^|[[:space:]])(deploy|ssh):|appleboy|digitalocean|droplet|ghcr\.io' .github/workflows/ci.yml
```

Expected: YAML is valid; the first search shows every preserved hardening, module check, readonly compile gate, quiet fail-closed secret-pattern scan, and pinned database digest; the second search has no output and exits 1.

- [ ] **Step 12: Commit runtime, docs, packaging, and CI as reviewable units**

```bash
git add .env.example README.md docs/security-and-secrets.md cmd/server/main.go cmd/server/main_test.go
git commit -m "feat: wire the identity server runtime"
git add .dockerignore Dockerfile
git commit -m "build: package the migration executable"
git add .github/workflows/ci.yml
git commit -m "ci: test identity flows with PostgreSQL"
```

Expected: three exact commits separate runtime/docs, container packaging, and CI. No deployment, release, or remote action occurs.

## Task 13: Run Phase 2 acceptance and leave a precise handoff

**Files:**

- Verify only; no source change expected.

- [ ] **Step 1: Start pinned PostgreSQL and prove disposable-database cleanup**

```bash
/bin/bash <<'BASH'
set -euo pipefail
cd /Users/blanc/Documents/Project/mcpaste
docker compose up -d --wait --wait-timeout 60 postgres
docker compose exec -T postgres pg_isready -U mcpaste -d postgres >/dev/null

acceptance_database="mcpaste_acceptance_preflight_$(date +%s)_$$"
if [[ ! "$acceptance_database" =~ ^mcpaste_acceptance_preflight_[0-9]+_[0-9]+$ ]]; then
  printf '%s\n' 'Generated acceptance database name is invalid.' >&2
  exit 1
fi
acceptance_database_created=0
cleanup() {
  cleanup_status=$?
  trap - EXIT INT TERM
  cleanup_failed=0
  if [[ "$acceptance_database_created" -eq 1 ]]; then
    if ! docker compose exec -T postgres dropdb -U mcpaste --maintenance-db=postgres --force --if-exists "$acceptance_database" >/dev/null 2>&1; then
      cleanup_failed=1
      printf '%s\n' 'Acceptance database cleanup failed.' >&2
    fi
  fi
  if [[ "$cleanup_status" -eq 0 && "$cleanup_failed" -ne 0 ]]; then
    cleanup_status=1
  fi
  exit "$cleanup_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if docker compose exec -T postgres psql -U mcpaste -d postgres -Atc "select 1 from pg_database where datname = '$acceptance_database'" | grep -qx '1'; then
  printf '%s\n' 'Disposable acceptance database already exists.' >&2
  exit 1
fi
docker compose exec -T postgres createdb -U mcpaste --maintenance-db=postgres "$acceptance_database"
acceptance_database_created=1
test "$(docker compose exec -T postgres psql -U mcpaste -d postgres -Atc "select count(*) from pg_database where datname = '$acceptance_database'")" = "1"
BASH
```

Expected: Compose waits at most 60 seconds for the defined PostgreSQL healthcheck, the post-wait `pg_isready` assertion passes, and the validated unique database is created. The EXIT trap force-drops exactly that database. A force-drop failure prints only `Acceptance database cleanup failed.`; it changes a successful block to exit 1 and preserves an already-nonzero block status. This block neither reads `.env.local` nor changes the retained developer database.

- [ ] **Step 2: Verify migration status, checksum, down boundary, and re-up**

```bash
/bin/bash <<'BASH'
set -euo pipefail
cd /Users/blanc/Documents/Project/mcpaste
docker compose up -d --wait --wait-timeout 60 postgres
docker compose exec -T postgres pg_isready -U mcpaste -d postgres >/dev/null

acceptance_database="mcpaste_acceptance_migration_$(date +%s)_$$"
if [[ ! "$acceptance_database" =~ ^mcpaste_acceptance_migration_[0-9]+_[0-9]+$ ]]; then
  printf '%s\n' 'Generated acceptance database name is invalid.' >&2
  exit 1
fi
acceptance_database_created=0
cleanup() {
  cleanup_status=$?
  trap - EXIT INT TERM
  unset MCPASTE_DATABASE_URL MCPASTE_TEST_DATABASE_URL MCPASTE_ENCRYPTION_KEYS MCPASTE_ACTIVE_KEY_ID
  cleanup_failed=0
  if [[ "$acceptance_database_created" -eq 1 ]]; then
    if ! docker compose exec -T postgres dropdb -U mcpaste --maintenance-db=postgres --force --if-exists "$acceptance_database" >/dev/null 2>&1; then
      cleanup_failed=1
      printf '%s\n' 'Acceptance database cleanup failed.' >&2
    fi
  fi
  if [[ "$cleanup_status" -eq 0 && "$cleanup_failed" -ne 0 ]]; then
    cleanup_status=1
  fi
  exit "$cleanup_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker compose exec -T postgres createdb -U mcpaste --maintenance-db=postgres "$acceptance_database"
acceptance_database_created=1
export MCPASTE_DATABASE_URL="postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/${acceptance_database}?sslmode=disable"
export MCPASTE_TEST_DATABASE_URL="$MCPASTE_DATABASE_URL"
acceptance_key="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')"
export MCPASTE_ACTIVE_KEY_ID=acceptance-migration-v1
export MCPASTE_ENCRYPTION_KEYS="${MCPASTE_ACTIVE_KEY_ID}:${acceptance_key}"
unset acceptance_key

GOFLAGS=-mod=readonly go run ./cmd/migrate up
test "$(GOFLAGS=-mod=readonly go run ./cmd/migrate status)" = "applied=1 available=1"
test "$(GOFLAGS=-mod=readonly go run ./cmd/migrate verify)" = "applied=1 available=1"
if GOFLAGS=-mod=readonly go run ./cmd/migrate down --steps 2; then
  exit 1
fi
GOFLAGS=-mod=readonly go run ./cmd/migrate down --steps 1
GOFLAGS=-mod=readonly go run ./cmd/migrate up
test "$(GOFLAGS=-mod=readonly go run ./cmd/migrate verify)" = "applied=1 available=1"
BASH
```

Expected: Compose's bounded health wait and the post-wait assertion pass; status and verify match `applied=1 available=1`; the two-step down is rejected without state change; the explicit one-step down and re-up succeed only in the disposable database. The trap unsets the ephemeral keyring and force-drops the database, destroying all migration and identity state from this block. A force-drop failure prints only `Acceptance database cleanup failed.`; it changes a successful block to exit 1 and preserves an already-nonzero block status.

- [ ] **Step 3: Run formatting, module, static, race, integration, and vulnerability gates**

```bash
/bin/bash <<'BASH'
set -euo pipefail
cd /Users/blanc/Documents/Project/mcpaste
docker compose up -d --wait --wait-timeout 60 postgres
docker compose exec -T postgres pg_isready -U mcpaste -d postgres >/dev/null

acceptance_database="mcpaste_acceptance_gate_$(date +%s)_$$"
if [[ ! "$acceptance_database" =~ ^mcpaste_acceptance_gate_[0-9]+_[0-9]+$ ]]; then
  printf '%s\n' 'Generated acceptance database name is invalid.' >&2
  exit 1
fi
acceptance_database_created=0
cleanup() {
  cleanup_status=$?
  trap - EXIT INT TERM
  unset MCPASTE_DATABASE_URL MCPASTE_TEST_DATABASE_URL MCPASTE_ENCRYPTION_KEYS MCPASTE_ACTIVE_KEY_ID
  cleanup_failed=0
  if [[ "$acceptance_database_created" -eq 1 ]]; then
    if ! docker compose exec -T postgres dropdb -U mcpaste --maintenance-db=postgres --force --if-exists "$acceptance_database" >/dev/null 2>&1; then
      cleanup_failed=1
      printf '%s\n' 'Acceptance database cleanup failed.' >&2
    fi
  fi
  if [[ "$cleanup_status" -eq 0 && "$cleanup_failed" -ne 0 ]]; then
    cleanup_status=1
  fi
  exit "$cleanup_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker compose exec -T postgres createdb -U mcpaste --maintenance-db=postgres "$acceptance_database"
acceptance_database_created=1
export MCPASTE_DATABASE_URL="postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/${acceptance_database}?sslmode=disable"
export MCPASTE_TEST_DATABASE_URL="$MCPASTE_DATABASE_URL"
acceptance_key="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')"
export MCPASTE_ACTIVE_KEY_ID=acceptance-gate-v1
export MCPASTE_ENCRYPTION_KEYS="${MCPASTE_ACTIVE_KEY_ID}:${acceptance_key}"
unset acceptance_key

test -z "$(find cmd internal db -type f -name '*.go' -exec gofmt -l {} +)"
go mod tidy -diff
go mod verify
GOFLAGS=-mod=readonly go run ./cmd/migrate up
GOFLAGS=-mod=readonly go vet ./cmd/migrate ./cmd/server ./db/migrations ./internal/config ./internal/database ./internal/database/migrate ./internal/httpserver ./internal/identity ./internal/identity/postgres ./internal/secure ./internal/testdb
GOFLAGS=-mod=readonly go test -race -count=1 -coverprofile=coverage.out ./cmd/migrate ./cmd/server ./db/migrations ./internal/config ./internal/database ./internal/database/migrate ./internal/httpserver ./internal/identity ./internal/identity/postgres ./internal/secure ./internal/testdb
GOFLAGS=-mod=readonly go build -o /tmp/mcpaste-server ./cmd/server
GOFLAGS=-mod=readonly go build -o /tmp/mcpaste-migrate ./cmd/migrate
GOFLAGS=-mod=readonly go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./cmd/migrate ./cmd/server ./db/migrations ./internal/config ./internal/database ./internal/database/migrate ./internal/httpserver ./internal/identity ./internal/identity/postgres ./internal/secure ./internal/testdb
BASH
```

Expected: Compose's bounded health wait and the post-wait assertion pass; every command exits 0; `go mod tidy -diff` emits no diff; module verification succeeds; every compile/test/vet/build command uses readonly module mode; all PostgreSQL integration tests execute against the disposable database; Govulncheck reports no reachable known vulnerability. The trap force-drops all test records and schemas afterward. A force-drop failure prints only `Acceptance database cleanup failed.`; it changes a successful block to exit 1 and preserves an already-nonzero block status.

- [ ] **Step 4: Run named security acceptance tests verbosely**

```bash
/bin/bash <<'BASH'
set -euo pipefail
cd /Users/blanc/Documents/Project/mcpaste
docker compose up -d --wait --wait-timeout 60 postgres
docker compose exec -T postgres pg_isready -U mcpaste -d postgres >/dev/null

acceptance_database="mcpaste_acceptance_named_$(date +%s)_$$"
if [[ ! "$acceptance_database" =~ ^mcpaste_acceptance_named_[0-9]+_[0-9]+$ ]]; then
  printf '%s\n' 'Generated acceptance database name is invalid.' >&2
  exit 1
fi
acceptance_database_created=0
cleanup() {
  cleanup_status=$?
  trap - EXIT INT TERM
  unset MCPASTE_DATABASE_URL MCPASTE_TEST_DATABASE_URL MCPASTE_ENCRYPTION_KEYS MCPASTE_ACTIVE_KEY_ID
  cleanup_failed=0
  if [[ "$acceptance_database_created" -eq 1 ]]; then
    if ! docker compose exec -T postgres dropdb -U mcpaste --maintenance-db=postgres --force --if-exists "$acceptance_database" >/dev/null 2>&1; then
      cleanup_failed=1
      printf '%s\n' 'Acceptance database cleanup failed.' >&2
    fi
  fi
  if [[ "$cleanup_status" -eq 0 && "$cleanup_failed" -ne 0 ]]; then
    cleanup_status=1
  fi
  exit "$cleanup_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker compose exec -T postgres createdb -U mcpaste --maintenance-db=postgres "$acceptance_database"
acceptance_database_created=1
export MCPASTE_DATABASE_URL="postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/${acceptance_database}?sslmode=disable"
export MCPASTE_TEST_DATABASE_URL="$MCPASTE_DATABASE_URL"
acceptance_key="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')"
export MCPASTE_ACTIVE_KEY_ID=acceptance-named-v1
export MCPASTE_ENCRYPTION_KEYS="${MCPASTE_ACTIVE_KEY_ID}:${acceptance_key}"
unset acceptance_key

GOFLAGS=-mod=readonly go run ./cmd/migrate up
GOFLAGS=-mod=readonly go test -race -count=1 ./internal/secure -run 'TestEnvelope|TestCredential|TestClaim|TestArgon2|TestProductionArgon2|TestRecovery|TestNewUUID' -v
GOFLAGS=-mod=readonly go test -race -count=1 ./internal/identity -run 'TestThirdRecoveryCancelsBeforeDatabaseWhileTwoPermitsAreHeld' -v
GOFLAGS=-mod=readonly go test -race -count=1 ./internal/identity/postgres -run 'TestWorkspaceScoped|TestDeviceName|TestIdempotency|TestPairing|TestRename|TestCleanup|TestClaimAndCleanup' -v
GOFLAGS=-mod=readonly go test -race -count=1 ./internal/httpserver -run 'TestIdentityLifecycle|TestPairingExpiry|TestFixedRateLimitPolicies|TestIdempotentMutationReplay|TestDatabaseBacked|TestNewAccessLogMiddleware|TestNewRecoveryMiddleware' -v
BASH
```

Expected: Compose's bounded health wait and the post-wait assertion pass; every named test passes in readonly module mode and output contains no runtime bearer, claim secret, recovery code, short code, QR payload, keyring, database URL, or authorization value. The trap unsets the ephemeral keyring and force-drops the disposable database, including every test schema and identity row. A force-drop failure prints only `Acceptance database cleanup failed.`; it changes a successful block to exit 1 and preserves an already-nonzero block status.

- [ ] **Step 5: Smoke-test server readiness and one anonymous boundary without printing secrets**

```bash
/bin/bash <<'BASH'
set -euo pipefail
cd /Users/blanc/Documents/Project/mcpaste
acceptance_port=18082
if ! command -v lsof >/dev/null 2>&1; then
  printf '%s\n' 'Port preflight tool is unavailable.' >&2
  exit 1
fi
listener_status=0
listener_output="$(lsof -nP -iTCP:"$acceptance_port" -sTCP:LISTEN 2>/dev/null)" || listener_status=$?
if [[ "$listener_status" -gt 1 ]]; then
  printf '%s\n' 'Acceptance port preflight failed.' >&2
  exit 1
fi
if [[ -n "$listener_output" ]]; then
  printf '%s\n' 'Dedicated acceptance port is already in use.' >&2
  exit 1
fi
unset listener_output listener_status

docker compose up -d --wait --wait-timeout 60 postgres
docker compose exec -T postgres pg_isready -U mcpaste -d postgres >/dev/null
acceptance_database="mcpaste_acceptance_smoke_$(date +%s)_$$"
if [[ ! "$acceptance_database" =~ ^mcpaste_acceptance_smoke_[0-9]+_[0-9]+$ ]]; then
  printf '%s\n' 'Generated acceptance database name is invalid.' >&2
  exit 1
fi
acceptance_database_created=0
acceptance_temp_dir="$(mktemp -d)"
acceptance_body_file="$acceptance_temp_dir/workspace-response.json"
mcpaste_acceptance_log="$acceptance_temp_dir/server.log"
mcpaste_acceptance_binary="$acceptance_temp_dir/mcpaste-server"
mcpaste_acceptance_pid=''
cleanup() {
  cleanup_status=$?
  trap - EXIT INT TERM
  if [[ -n "$mcpaste_acceptance_pid" ]]; then
    if kill -0 "$mcpaste_acceptance_pid" 2>/dev/null; then
      kill "$mcpaste_acceptance_pid" 2>/dev/null || true
    fi
    wait "$mcpaste_acceptance_pid" 2>/dev/null || true
  fi
  unset MCPASTE_DATABASE_URL MCPASTE_TEST_DATABASE_URL MCPASTE_ENCRYPTION_KEYS MCPASTE_ACTIVE_KEY_ID MCPASTE_HTTP_ADDR
  cleanup_failed=0
  if [[ "$acceptance_database_created" -eq 1 ]]; then
    if ! docker compose exec -T postgres dropdb -U mcpaste --maintenance-db=postgres --force --if-exists "$acceptance_database" >/dev/null 2>&1; then
      cleanup_failed=1
      printf '%s\n' 'Acceptance database cleanup failed.' >&2
    fi
  fi
  if [[ "$cleanup_status" -eq 0 && "$cleanup_failed" -ne 0 ]]; then
    cleanup_status=1
  fi
  rm -f "$acceptance_body_file" "$mcpaste_acceptance_log" "$mcpaste_acceptance_binary"
  rmdir "$acceptance_temp_dir" 2>/dev/null || true
  exit "$cleanup_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker compose exec -T postgres createdb -U mcpaste --maintenance-db=postgres "$acceptance_database"
acceptance_database_created=1
export MCPASTE_DATABASE_URL="postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/${acceptance_database}?sslmode=disable"
export MCPASTE_TEST_DATABASE_URL="$MCPASTE_DATABASE_URL"
acceptance_key="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')"
export MCPASTE_ACTIVE_KEY_ID=acceptance-smoke-v1
export MCPASTE_ENCRYPTION_KEYS="${MCPASTE_ACTIVE_KEY_ID}:${acceptance_key}"
export MCPASTE_HTTP_ADDR="127.0.0.1:${acceptance_port}"
unset acceptance_key

GOFLAGS=-mod=readonly go run ./cmd/migrate up
GOFLAGS=-mod=readonly go build -o "$mcpaste_acceptance_binary" ./cmd/server
"$mcpaste_acceptance_binary" >"$mcpaste_acceptance_log" 2>&1 &
mcpaste_acceptance_pid=$!

acceptance_server_owns_listener() {
  if [[ -z "$mcpaste_acceptance_pid" ]] || ! kill -0 "$mcpaste_acceptance_pid" 2>/dev/null; then
	return 1
  fi
  owner_status=0
  owner_output="$(lsof -nP -a -p "$mcpaste_acceptance_pid" -iTCP:"$acceptance_port" -sTCP:LISTEN -t 2>/dev/null)" || owner_status=$?
  [[ "$owner_status" -eq 0 && "$owner_output" == "$mcpaste_acceptance_pid" ]]
}

require_acceptance_server() {
  if ! acceptance_server_owns_listener; then
    printf '%s\n' 'Acceptance server is unavailable.' >&2
    return 1
  fi
}

listener_owned=0
for attempt in $(seq 1 20); do
  if [[ -z "$mcpaste_acceptance_pid" ]] || ! kill -0 "$mcpaste_acceptance_pid" 2>/dev/null; then
    printf '%s\n' 'Acceptance server is unavailable.' >&2
    exit 1
  fi
  if acceptance_server_owns_listener; then
    listener_owned=1
    break
  fi
  sleep 1
done
if [[ "$listener_owned" -ne 1 ]]; then
  printf '%s\n' 'Acceptance server is unavailable.' >&2
  exit 1
fi

live_body=''
live_reached=0
for attempt in $(seq 1 20); do
  require_acceptance_server
  if live_body="$(curl --fail --silent "http://127.0.0.1:${acceptance_port}/livez" 2>/dev/null)"; then
    live_reached=1
    break
  fi
  sleep 1
done
test "$live_reached" = "1"
require_acceptance_server
ready_body="$(curl --fail --silent "http://127.0.0.1:${acceptance_port}/readyz")"
test "$live_body" = '{"status":"ok"}'
test "$ready_body" = '{"status":"ok"}'
require_acceptance_server
if ! curl --fail --silent \
  --output "$acceptance_body_file" \
  --header 'Content-Type: application/json' \
  --header 'Idempotency-Key: 00000000-0000-4000-8000-000000009999' \
  --data '{"device_name":"Acceptance Mac","platform":"macos"}' \
  "http://127.0.0.1:${acceptance_port}/v1/workspaces"
then
  printf '%s\n' 'Credential-bearing acceptance request failed.' >&2
  exit 1
fi
test "$(jq '.credentials | length' "$acceptance_body_file")" = "2"
test "$(jq -r '.credentials[0].kind' "$acceptance_body_file")" = "full"
test "$(jq -r '.credentials[1].kind' "$acceptance_body_file")" = "connector"
test "$(jq -r '.device.role' "$acceptance_body_file")" = "full"
jq -e '(.workspace_id | type == "string") and (.recovery_code | type == "string")' "$acceptance_body_file" >/dev/null
require_acceptance_server
kill "$mcpaste_acceptance_pid"
if ! wait "$mcpaste_acceptance_pid"; then
  printf '%s\n' 'Acceptance server did not stop cleanly.' >&2
  exit 1
fi
mcpaste_acceptance_pid=''
BASH
```

Expected: Compose's bounded health wait and the post-wait assertion pass. The fail-closed preflight proves loopback port 18082 is free without stopping another process. Startup waits at most 20 seconds for `lsof` to prove the exact spawned PID owns that LISTEN socket. Every health or credential-bearing curl is preceded immediately by both that ownership proof and `kill -0`; loss of either emits only `Acceptance server is unavailable.` and prevents the request. The post-request guard proves the same process still owns the socket before shutdown. Both health bodies match exactly, all `jq` assertions pass without printing response content, and graceful termination is waited. The trap preserves the first failure status, safely kills/waits any surviving exact PID, unsets the ephemeral keyring, force-drops the disposable database, and deletes the credential-bearing body, log, binary, and temporary directory. A force-drop failure prints only `Acceptance database cleanup failed.`; it changes a successful block to exit 1 and preserves an already-nonzero block status.

- [ ] **Step 6: Verify the container and migration executable boundary**

```bash
/bin/bash <<'BASH'
set -euo pipefail
cd /Users/blanc/Documents/Project/mcpaste
mcpaste_container_name="mcpaste-phase2-inspect-$$"
cleanup() {
  cleanup_status=$?
  trap - EXIT INT TERM
  docker rm -f "$mcpaste_container_name" >/dev/null 2>&1 || true
  exit "$cleanup_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker build -t mcpaste-server:phase2 .
docker image inspect mcpaste-server:phase2 --format '{{.Config.User}} {{json .Config.Entrypoint}}'
docker create --name "$mcpaste_container_name" mcpaste-server:phase2 >/dev/null
container_files="$(docker export "$mcpaste_container_name" | tar -tf - | sort)"
printf '%s\n' "$container_files"
grep -qx 'mcpaste-server' <<<"$container_files"
grep -qx 'mcpaste-migrate' <<<"$container_files"
if grep -Eq '(^|/)(\.env|\.git|go\.mod|README\.md|docs|db)(/|$)' <<<"$container_files"; then
  exit 1
fi
docker rm "$mcpaste_container_name" >/dev/null
BASH
```

Expected: image metadata prints `65532:65532 ["/mcpaste-server"]`; exported file names include `mcpaste-server`, `mcpaste-migrate`, and CA certificates, with no source, `.env`, Git, documentation, database, or key file.

- [ ] **Step 7: Verify schema and scope boundaries**

```bash
/bin/bash <<'BASH'
set -euo pipefail
cd /Users/blanc/Documents/Project/mcpaste
docker compose up -d --wait --wait-timeout 60 postgres
docker compose exec -T postgres pg_isready -U mcpaste -d postgres >/dev/null

acceptance_database="mcpaste_acceptance_schema_$(date +%s)_$$"
if [[ ! "$acceptance_database" =~ ^mcpaste_acceptance_schema_[0-9]+_[0-9]+$ ]]; then
  printf '%s\n' 'Generated acceptance database name is invalid.' >&2
  exit 1
fi
acceptance_database_created=0
cleanup() {
  cleanup_status=$?
  trap - EXIT INT TERM
  unset MCPASTE_DATABASE_URL MCPASTE_TEST_DATABASE_URL MCPASTE_ENCRYPTION_KEYS MCPASTE_ACTIVE_KEY_ID
  cleanup_failed=0
  if [[ "$acceptance_database_created" -eq 1 ]]; then
    if ! docker compose exec -T postgres dropdb -U mcpaste --maintenance-db=postgres --force --if-exists "$acceptance_database" >/dev/null 2>&1; then
      cleanup_failed=1
      printf '%s\n' 'Acceptance database cleanup failed.' >&2
    fi
  fi
  if [[ "$cleanup_status" -eq 0 && "$cleanup_failed" -ne 0 ]]; then
    cleanup_status=1
  fi
  exit "$cleanup_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker compose exec -T postgres createdb -U mcpaste --maintenance-db=postgres "$acceptance_database"
acceptance_database_created=1
export MCPASTE_DATABASE_URL="postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/${acceptance_database}?sslmode=disable"
export MCPASTE_TEST_DATABASE_URL="$MCPASTE_DATABASE_URL"
acceptance_key="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')"
export MCPASTE_ACTIVE_KEY_ID=acceptance-schema-v1
export MCPASTE_ENCRYPTION_KEYS="${MCPASTE_ACTIVE_KEY_ID}:${acceptance_key}"
unset acceptance_key

GOFLAGS=-mod=readonly go run ./cmd/migrate up
docker compose exec -T postgres psql -U mcpaste -d "$acceptance_database" -Atc "select tablename from pg_tables where schemaname='public' order by tablename"
if rg -n 'create table (pastes|text_revisions|images)|/v1/(pastes|mcp|sse)|streamable' db/migrations internal/httpserver/api.go; then
  exit 1
fi
rg -n 'workspace_id = \$1|workspace_id = \$2' internal/identity/postgres
BASH
```

Expected: Compose's bounded health wait and the post-wait assertion pass; database tables in the disposable database are only the Phase 2 identity, event, idempotency, rate-limit, and migration tables. The first source search has no feature table or route match; incidental module/product names are reviewed and are not APIs. The workspace predicate search covers every established-workspace store file. The trap unsets the ephemeral keyring and force-drops the database. A force-drop failure prints only `Acceptance database cleanup failed.`; it changes a successful block to exit 1 and preserves an already-nonzero block status.

- [ ] **Step 8: Verify logs, placeholders, accidental secrets, workflow hardening, and whitespace**

```bash
/bin/bash <<'BASH'
set -euo pipefail
cd /Users/blanc/Documents/Project/mcpaste
acceptance_port=18082
if ! command -v lsof >/dev/null 2>&1; then
  printf '%s\n' 'Port preflight tool is unavailable.' >&2
  exit 1
fi
listener_status=0
listener_output="$(lsof -nP -iTCP:"$acceptance_port" -sTCP:LISTEN 2>/dev/null)" || listener_status=$?
if [[ "$listener_status" -gt 1 ]]; then
  printf '%s\n' 'Acceptance port preflight failed.' >&2
  exit 1
fi
if [[ -n "$listener_output" ]]; then
  printf '%s\n' 'Dedicated acceptance port is already in use.' >&2
  exit 1
fi
unset listener_output listener_status

docker compose up -d --wait --wait-timeout 60 postgres
docker compose exec -T postgres pg_isready -U mcpaste -d postgres >/dev/null
acceptance_database="mcpaste_acceptance_log_$(date +%s)_$$"
if [[ ! "$acceptance_database" =~ ^mcpaste_acceptance_log_[0-9]+_[0-9]+$ ]]; then
  printf '%s\n' 'Generated acceptance database name is invalid.' >&2
  exit 1
fi
acceptance_database_created=0
acceptance_temp_dir="$(mktemp -d)"
mcpaste_log_file="$acceptance_temp_dir/server.log"
mcpaste_error_body="$acceptance_temp_dir/error-response.json"
mcpaste_log_binary="$acceptance_temp_dir/mcpaste-server"
mcpaste_log_pid=''
cleanup() {
  cleanup_status=$?
  trap - EXIT INT TERM
  if [[ -n "$mcpaste_log_pid" ]]; then
    if kill -0 "$mcpaste_log_pid" 2>/dev/null; then
      kill "$mcpaste_log_pid" 2>/dev/null || true
    fi
    wait "$mcpaste_log_pid" 2>/dev/null || true
  fi
  unset MCPASTE_DATABASE_URL MCPASTE_TEST_DATABASE_URL MCPASTE_ENCRYPTION_KEYS MCPASTE_ACTIVE_KEY_ID MCPASTE_HTTP_ADDR
  cleanup_failed=0
  if [[ "$acceptance_database_created" -eq 1 ]]; then
    if ! docker compose exec -T postgres dropdb -U mcpaste --maintenance-db=postgres --force --if-exists "$acceptance_database" >/dev/null 2>&1; then
      cleanup_failed=1
      printf '%s\n' 'Acceptance database cleanup failed.' >&2
    fi
  fi
  if [[ "$cleanup_status" -eq 0 && "$cleanup_failed" -ne 0 ]]; then
    cleanup_status=1
  fi
  rm -f "$mcpaste_log_file" "$mcpaste_error_body" "$mcpaste_log_binary"
  rmdir "$acceptance_temp_dir" 2>/dev/null || true
  exit "$cleanup_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker compose exec -T postgres createdb -U mcpaste --maintenance-db=postgres "$acceptance_database"
acceptance_database_created=1
export MCPASTE_DATABASE_URL="postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/${acceptance_database}?sslmode=disable"
export MCPASTE_TEST_DATABASE_URL="$MCPASTE_DATABASE_URL"
acceptance_key="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')"
export MCPASTE_ACTIVE_KEY_ID=acceptance-log-v1
export MCPASTE_ENCRYPTION_KEYS="${MCPASTE_ACTIVE_KEY_ID}:${acceptance_key}"
export MCPASTE_HTTP_ADDR="127.0.0.1:${acceptance_port}"
unset acceptance_key

GOFLAGS=-mod=readonly go run ./cmd/migrate up
GOFLAGS=-mod=readonly go build -o "$mcpaste_log_binary" ./cmd/server
"$mcpaste_log_binary" >"$mcpaste_log_file" 2>&1 &
mcpaste_log_pid=$!

log_server_owns_listener() {
  if [[ -z "$mcpaste_log_pid" ]] || ! kill -0 "$mcpaste_log_pid" 2>/dev/null; then
	return 1
  fi
  owner_status=0
  owner_output="$(lsof -nP -a -p "$mcpaste_log_pid" -iTCP:"$acceptance_port" -sTCP:LISTEN -t 2>/dev/null)" || owner_status=$?
  [[ "$owner_status" -eq 0 && "$owner_output" == "$mcpaste_log_pid" ]]
}

require_log_server() {
  if ! log_server_owns_listener; then
    printf '%s\n' 'Acceptance server is unavailable.' >&2
    return 1
  fi
}

listener_owned=0
for attempt in $(seq 1 20); do
  if [[ -z "$mcpaste_log_pid" ]] || ! kill -0 "$mcpaste_log_pid" 2>/dev/null; then
    printf '%s\n' 'Acceptance server is unavailable.' >&2
    exit 1
  fi
  if log_server_owns_listener; then
    listener_owned=1
    break
  fi
  sleep 1
done
if [[ "$listener_owned" -ne 1 ]]; then
  printf '%s\n' 'Acceptance server is unavailable.' >&2
  exit 1
fi

live_reached=0
for attempt in $(seq 1 20); do
  require_log_server
  if curl --fail --silent "http://127.0.0.1:${acceptance_port}/livez" >/dev/null 2>&1; then
    live_reached=1
    break
  fi
  sleep 1
done
test "$live_reached" = "1"
require_log_server
test "$(curl --fail --silent "http://127.0.0.1:${acceptance_port}/readyz")" = '{"status":"ok"}'
require_log_server
if ! curl --silent \
  --output "$mcpaste_error_body" \
  --header 'Authorization: Bearer authorization-secret-marker' \
  --header 'Idempotency-Key: idempotency-secret-marker' \
  --header 'Cookie: claim-secret-marker' \
  --data '{"short_code":"short-code-marker","recovery_code":"recovery-code-marker","qr_payload":"qr-payload-marker"}' \
  "http://127.0.0.1:${acceptance_port}/v1/not-a-route?query-secret-marker"
then
  printf '%s\n' 'Credential-bearing log-redaction request failed.' >&2
  exit 1
fi
test "$(cat "$mcpaste_error_body")" = '{"error":{"code":"not_found"}}'
require_log_server
kill "$mcpaste_log_pid"
if ! wait "$mcpaste_log_pid"; then
  printf '%s\n' 'Acceptance server did not stop cleanly.' >&2
  exit 1
fi
mcpaste_log_pid=''

log_scan_status=0
rg --quiet 'authorization-secret-marker|idempotency-secret-marker|claim-secret-marker|short-code-marker|recovery-code-marker|qr-payload-marker|query-secret-marker|Authorization|Idempotency-Key|claim_secret|recovery_code|short_code|qr_payload|MCPASTE_ENCRYPTION_KEYS|MCPASTE_DATABASE_URL' "$mcpaste_log_file" || log_scan_status=$?
if [[ "$log_scan_status" -eq 0 ]]; then
  printf '%s\n' 'Sensitive marker detected in server log.' >&2
  exit 1
fi
if [[ "$log_scan_status" -ne 1 ]]; then
  printf '%s\n' 'Server-log marker scan failed.' >&2
  exit 1
fi
placeholder_status=0
rg --quiet -i '\b(T[B]D|T[O]DO|F[I]XME|X[X]X)\b' cmd internal db README.md docs/security-and-secrets.md .github Dockerfile compose.yaml || placeholder_status=$?
if [[ "$placeholder_status" -eq 0 ]]; then
  printf '%s\n' 'Placeholder marker detected.' >&2
  exit 1
fi
if [[ "$placeholder_status" -ne 1 ]]; then
  printf '%s\n' 'Placeholder scan failed.' >&2
  exit 1
fi
secret_pattern_status=0
rg --quiet --hidden \
  --glob '!.git/**' \
  --glob '!.github/workflows/ci.yml' \
  --glob '!docs/security-and-secrets.md' \
  --glob '!docs/superpowers/plans/**' \
  '(sk-[A-Za-z0-9]{12,}|ghp_[A-Za-z0-9]{12,}|AKIA[0-9A-Z]{16}|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY)' . || secret_pattern_status=$?
if [[ "$secret_pattern_status" -eq 0 ]]; then
  printf '%s\n' 'Potential secret pattern detected.' >&2
  exit 1
fi
if [[ "$secret_pattern_status" -ne 1 ]]; then
  printf '%s\n' 'Secret-pattern scan failed.' >&2
  exit 1
fi
rg -n 'permissions:|contents: read|persist-credentials: false|sha256sum --check|go mod tidy -diff|go mod verify|GOFLAGS=-mod=readonly|rg --quiet' .github/workflows/ci.yml
if rg -n '(^|[[:space:]])(deploy|ssh):|appleboy|digitalocean|droplet|ghcr\.io' .github/workflows/ci.yml; then
  exit 1
fi
git diff --check HEAD
BASH
```

Expected: Compose's bounded health wait and the post-wait assertion pass. The fail-closed port preflight is followed by a bounded startup wait until `lsof` proves the exact spawned PID owns the LISTEN socket. Both `kill -0` and exact socket ownership are checked immediately before every health or credential-bearing curl and once after the credential-bearing response; loss of either emits only `Acceptance server is unavailable.` and prevents the request. The generic 404 body is captured without printing markers. Log, placeholder, and secret-pattern scans accept only `rg` status 1 as clean; status 0 or any scan error fails with generic metadata and never prints matches. Workflow hardening appears, deploy search prints nothing, and whitespace check exits 0. The trap safely kills/waits the exact PID, unsets the ephemeral keyring, force-drops the disposable database, and deletes the response, log, binary, and temporary directory. A force-drop failure prints only `Acceptance database cleanup failed.`; it changes a successful block to exit 1 and preserves an already-nonzero block status.

- [ ] **Step 9: Run a local checksum-verified full-history Gitleaks scan**

```bash
/bin/bash <<'BASH'
set -euo pipefail
cd /Users/blanc/Documents/Project/mcpaste
test "$(git rev-parse --is-shallow-repository)" = "false"

mcpaste_gitleaks_container="mcpaste-gitleaks-$$"
mcpaste_gitleaks_parent="$(mktemp -d)"
mcpaste_gitleaks_clone="$mcpaste_gitleaks_parent/repository"
cleanup() {
  cleanup_status=$?
  trap - EXIT INT TERM
  docker rm -f "$mcpaste_gitleaks_container" >/dev/null 2>&1 || true
  find "$mcpaste_gitleaks_parent" -depth ! -type d -delete
  find "$mcpaste_gitleaks_parent" -depth -type d -empty -delete
  exit "$cleanup_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

git clone --no-local . "$mcpaste_gitleaks_clone" >/dev/null
test "$(git -C "$mcpaste_gitleaks_clone" rev-parse --is-shallow-repository)" = "false"
test -z "$(git -C "$mcpaste_gitleaks_clone" status --porcelain)"
test ! -e "$mcpaste_gitleaks_clone/.env.local"
test ! -e "$mcpaste_gitleaks_clone/.env"
local_secret_path_status=0
find "$mcpaste_gitleaks_clone" \
  -path "$mcpaste_gitleaks_clone/.git" -prune -o \
  -type f \( -name '.env' -o -name '.env.local' -o -name '*.pem' -o -name '*.key' \) \
  -print -quit | grep --quiet . || local_secret_path_status=$?
if [[ "$local_secret_path_status" -eq 0 ]]; then
  printf '%s\n' 'Clean clone contains a forbidden local-secret path.' >&2
  exit 1
fi
if [[ "$local_secret_path_status" -ne 1 ]]; then
  printf '%s\n' 'Clean-clone secret-path scan failed.' >&2
  exit 1
fi

docker run --name "$mcpaste_gitleaks_container" \
  --platform linux/amd64 \
  --volume "$mcpaste_gitleaks_clone:/repo:ro" \
  --workdir /repo \
  alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce sh -euc '
    apk add --no-cache ca-certificates curl git tar >/dev/null
    curl --fail --location --silent --show-error \
      --output /tmp/gitleaks.tar.gz \
      https://github.com/gitleaks/gitleaks/releases/download/v8.24.3/gitleaks_8.24.3_linux_x64.tar.gz
    echo "9991e0b2903da4c8f6122b5c3186448b927a5da4deef1fe45271c3793f4ee29c  /tmp/gitleaks.tar.gz" | sha256sum -c
    tar -xzf /tmp/gitleaks.tar.gz -C /tmp gitleaks
    git config --global --add safe.directory /repo
    if ! /tmp/gitleaks detect --source /repo --redact >/tmp/gitleaks-result 2>&1; then
      printf "%s\n" "Gitleaks detected a secret or failed to scan." >&2
      exit 1
    fi
  '
BASH
```

Expected: the source and disposable `--no-local` clone are non-shallow, the clone is clean, and `.env.local`, `.env`, PEM files, and key files are absent outside `.git`. Only that tracked-history clone is mounted read-only; the working tree containing ignored local state is never mounted. The Alpine 3.22.5 multi-architecture index is pinned by digest, its portable `sha256sum -c` invocation verifies the downloaded Gitleaks v8.24.3 archive, and the full-history scan exits 0. A finding or scanner error emits only the generic failure line, never match content. The trap removes the container and every file/directory in the disposable clone parent.

- [ ] **Step 10: Verify signal exits plus commit and working-tree boundaries**

```bash
/bin/bash <<'BASH'
set -euo pipefail
for signal_case in 'INT 130' 'TERM 143'; do
  read -r signal_name expected_status <<<"$signal_case"
  set +e
  /bin/bash -s -- "$signal_name" <<'SIGNAL_BASH'
set -euo pipefail
cleanup() {
  cleanup_status=$?
  trap - EXIT INT TERM
  exit "$cleanup_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
kill -s "$1" "$$"
exit 99
SIGNAL_BASH
  actual_status=$?
  set -e
  if [[ "$actual_status" -ne "$expected_status" ]]; then
    printf '%s\n' 'Signal cleanup status check failed.' >&2
    exit 1
  fi
done
cd /Users/blanc/Documents/Project/mcpaste
if [[ -n "$(git status --porcelain=v1 --untracked-files=all)" ]]; then
  printf '%s\n' 'Phase 2 acceptance requires a clean worktree.' >&2
  exit 1
fi
git diff --quiet
git diff --cached --quiet
test "$(git show -s --format=%s HEAD~13)" = 'docs: record foundation and plan identity server'
expected_subjects="$(printf '%s\n' \
  'build: add PostgreSQL identity dependencies' \
  'feat: add identity schema migrations' \
  'feat: add identity security primitives' \
  'feat: add PostgreSQL server configuration' \
  'feat: define identity domain contracts' \
  'feat: add identity PostgreSQL repository core' \
  'feat: persist pairing and device lifecycle' \
  'feat: orchestrate encrypted identity issuance' \
  'feat: expose identity HTTP API' \
  'test: cover identity lifecycle security' \
  'feat: wire the identity server runtime' \
  'build: package the migration executable' \
  'ci: test identity flows with PostgreSQL')"
actual_subjects="$(git log --format=%s --reverse HEAD~13..HEAD)"
if [[ "$actual_subjects" != "$expected_subjects" ]]; then
  printf '%s\n' 'Phase 2 commit sequence is invalid.' >&2
  exit 1
fi
BASH
```

Expected: the focused subshell exits 130 for `INT` and 143 for `TERM`, proving the signal handlers convert interruption to failure before the EXIT cleanup preserves that status. Tracked, staged, and untracked worktree state is empty; ignored `coverage.out` may remain. `HEAD~13` has the exact documentation handoff subject `docs: record foundation and plan identity server`, and the latest 13 subjects byte-match the listed Phase 2 sequence in chronological order. A dirty tree, missing baseline, extra/reordered/squashed commit, or subject mismatch exits nonzero with generic metadata. Nothing is pushed, deployed, tagged, released, or added to a remote.

- [ ] **Step 11: Record the execution handoff in the final response**

Report these fields without copying secrets:

- implemented: exact commits and files changed by Tasks 1 through 12;
- verified: exact commands from Steps 2 through 10 and their pass/fail status;
- committed: commit identifiers for every planned commit;
- pushed/deployed: `none`;
- deviations: original step, changed action, reason, and replacement evidence, or `none`;
- remaining risks: single-node PostgreSQL, fixed-window limits, encrypted replay rows readable by a fully compromised server, and no backup by approved design;
- next safe step: write or refresh Phase 3 against the resulting schema and APIs without beginning it in this phase.

## Phase 3 handoff contract

Phase 3 may rely on the following and nothing broader:

- authenticated principals provide explicit workspace, device, and scope IDs;
- connector scope is enforced read-only and full Mac installations hold a separate connector credential;
- `workspace_events` allocates monotonically increasing workspace-local sequence numbers and retains metadata for 35 days, but no cursor or delivery route exists yet;
- `idempotency_records` can store encrypted exact responses, but Phase 3 must define distinct operation names and canonical paste request hashes;
- AES envelopes accept purpose and stable object ID as associated data; Phase 3 must use new purposes for text bodies and must not reuse identity-purpose strings;
- migrations are ordered and checksum-verified; Phase 3 starts at `000002` and remains expand-and-contract compatible;
- device revocation and recovery rotation are complete; Phase 3 must not add an account/password model or change anonymous workspace identity incidentally.

Do not claim text, MCP, SSE, sync, image, macOS, Linux, deployment, or production readiness at this handoff. Those remain roadmap work.

## Security invariant review checklist

Before calling Phase 2 complete, the execution worker must answer each item with a test name or SQL constraint:

- [ ] No bearer, claim, or recovery secret column exists; hashes are exactly 32 bytes.
- [ ] Token and recovery wire formats expose only workspace UUID plus random non-secret locator before the 256-bit secret.
- [ ] AES writes always use a fresh 12-byte nonce and authenticates the active key ID in AAD; decrypt rejects ciphertext/context/key-ID tamper, unknown keys, and duplicate key material under another ID while retained old IDs still decrypt during rotation.
- [ ] Bearer locator/secret, claim secret, recovery locator/secret, and encryption-key segments all use `decodeCanonicalRawURL`, which checks the exact encoded length, calls `base64.RawURLEncoding.Strict()`, and requires encode-after-decode equality; every format rejects padding, non-zero trailing bits, CR, and LF with generic errors.
- [ ] The sole production `argon2.IDKey` call accepts exact `*RecoveryPermit`, an exported opaque concrete handle whose only field is an unexported shared-state pointer; no interface, dynamic accessor, or externally supplied wrapper is accepted. Nil and zero handles return `errInvalidRecoveryPermit` without panic. Copies share one limiter/mutex/released state, so the mutex covers each complete derivation and excludes `Release`; concurrent derivations serialize, release waits for active work and returns the slot exactly once, and sequential generation then verification reuses one held slot. Standalone calls acquire/release with context; recovery mutation acquires before any service transaction, precomputes rotation before mutation, passes the same exact concrete handle into row-locked verification without reacquiring, admits at most two recovery mutation transactions, and releases on every path.
- [ ] Production keyring values can enter only through process environment configuration.
- [ ] Full Mac grants contain exactly full then connector credentials; connector grants contain exactly connector.
- [ ] QR and short-code views never contain claim or bearer secret; approval never returns a credential.
- [ ] Pairing claim replay returns the same encrypted response bytes until exact claim expiry.
- [ ] Claim and cleanup serialize on the pairing row; cleanup atomically revokes credentials, appends `device.revoked`, and marks invalidation before claim can return.
- [ ] Recovery replay survives a dropped response while the old code is otherwise immediately invalid.
- [ ] An expired idempotency key is deleted and replaced in the same transaction before a new mutation, leaving one current row and no duplicate-key failure.
- [ ] Every established-workspace store method and SQL statement receives and predicates on workspace ID.
- [ ] Cross-workspace object IDs return generic `404`, not existence detail.
- [ ] Connector scope receives generic `403` on approval, list, rename, revoke, and every future write route.
- [ ] Self-revocation returns `400 invalid_request` before idempotency or mutation; a different active full device can revoke the target and replay that allowed revocation.
- [ ] JSON body byte 4,097, unknown fields, trailing values, duplicate auth/idempotency headers, and wrong media type fail generically.
- [ ] Every short-code byte belongs to `23456789ABCDEFGHJKMNPQRSTUVWXYZ`; malformed alphabet fails before rate-limit or pairing SQL.
- [ ] Rate rows contain irreversible subject hashes and return exact integer `Retry-After`.
- [ ] Access and panic logs contain route patterns and metadata only; every `/v1/` panic discards buffered partial output and returns the canonical JSON `internal_error`, while Foundation non-v1 recovery remains plain text.
- [ ] `/readyz` pings PostgreSQL and runs lock-free read-only `CheckCurrent` under one two-second bound, requires `applied == available > 0`, and never waits for the migration advisory lock or exposes database details.
- [ ] Cleanup revokes unclaimed expired grants and records workspace-local events before marking or deleting their replay metadata.
- [ ] Hosted CI runs `go mod tidy -diff` and `go mod verify`, and every compile/test/vet/build command uses `GOFLAGS=-mod=readonly`; secret-pattern checks accept only no-match status and never print matches.
- [ ] Final acceptance uses only per-block disposable databases and ephemeral keyrings; each Compose cold start uses `--wait --wait-timeout 60`; loopback port 18082 is preflighted; every acceptance curl is guarded by both `kill -0` and exact spawned-PID LISTEN ownership before the request and checked after credential-bearing responses; every cleanup registers EXIT separately, maps INT to 130 and TERM to 143, disables all three traps inside cleanup, and preserves fail-closed database-drop status promotion.
- [ ] Local Gitleaks clones tracked full history with `git clone --no-local`, proves local secret paths absent, mounts only that clone read-only into the digest-pinned scanner container, and suppresses finding content.
- [ ] The final Git gate requires no tracked, staged, or untracked changes, the exact documentation handoff subject at `HEAD~13`, and a byte-exact chronological list of all 13 planned implementation commit subjects.

## References

Primary sources and current metadata verified on 2026-08-12:

- Go 1.26 release documentation: <https://go.dev/doc/go1.26>
- Go `net/http` method-aware `ServeMux` patterns and `MaxBytesReader`: <https://pkg.go.dev/net/http>
- Go AES package: <https://pkg.go.dev/crypto/aes>
- Go GCM and nonce requirements: <https://pkg.go.dev/crypto/cipher>
- Go canonical raw URL-base64 primitives and strict trailing-bit validation: <https://pkg.go.dev/encoding/base64#Encoding.Strict>
- Go 1.26 cryptographic randomness behavior: <https://pkg.go.dev/crypto/rand>
- Go embedding: <https://pkg.go.dev/embed>
- Go module file and dependency commands: <https://go.dev/doc/modules/gomod-ref> and <https://go.dev/doc/modules/managing-dependencies>
- Official Go module metadata for pgx v5.10.0: <https://proxy.golang.org/github.com/jackc/pgx/v5/@v/v5.10.0.info> and <https://proxy.golang.org/github.com/jackc/pgx/v5/@v/v5.10.0.mod>
- pgx v5.10.0 transaction and pool documentation: <https://pkg.go.dev/github.com/jackc/pgx/v5@v5.10.0> and <https://pkg.go.dev/github.com/jackc/pgx/v5@v5.10.0/pgxpool>
- Official Go module metadata for x/crypto v0.55.0: <https://proxy.golang.org/golang.org/x/crypto/@v/v0.55.0.info> and <https://proxy.golang.org/golang.org/x/crypto/@v/v0.55.0.mod>
- Official Go module requirement showing x/text v0.41.0 selects x/sync v0.22.0: <https://proxy.golang.org/golang.org/x/text/@v/v0.41.0.mod>
- x/crypto v0.55.0 Argon2id API and RFC 9106 parameter guidance: <https://pkg.go.dev/golang.org/x/crypto@v0.55.0/argon2>
- PostgreSQL 18 transaction and advisory-lock behavior: <https://www.postgresql.org/docs/18/tutorial-transactions.html> and <https://www.postgresql.org/docs/18/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS>
- PostgreSQL 18 constraints and indexes: <https://www.postgresql.org/docs/18/ddl-constraints.html> and <https://www.postgresql.org/docs/18/indexes.html>
- PostgreSQL 18 UUID generation: <https://www.postgresql.org/docs/18/functions-uuid.html>
- Docker Official PostgreSQL image, including PostgreSQL 18 volume-path change: <https://hub.docker.com/_/postgres>
- Docker Official Golang and Alpine images: <https://hub.docker.com/_/golang> and <https://hub.docker.com/_/alpine>
- Docker registry digest inspection: <https://docs.docker.com/reference/cli/docker/buildx/imagetools/inspect/>
- Docker Compose environment behavior: <https://docs.docker.com/compose/how-tos/environment-variables/>
- GitHub Actions PostgreSQL service containers: <https://docs.github.com/actions/use-cases-and-examples/using-containerized-services/creating-postgresql-service-containers>
- Pinned checkout action source: <https://github.com/actions/checkout/tree/df4cb1c069e1874edd31b4311f1884172cec0e10>
- Pinned setup-go action source: <https://github.com/actions/setup-go/tree/4b73464bb391d4059bd26b0524d20df3927bd417>
- Checksum-verified Gitleaks v8.24.3 release: <https://github.com/gitleaks/gitleaks/releases/tag/v8.24.3>

The PostgreSQL image was verified as `18.4-alpine3.24` with multi-architecture index digest `sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15`; the official image annotations identify source revision `4f9ced003ba58a854656ba150d146243d27ae3ac`. Do not float the tag. During an intentional dependency refresh, run `docker buildx imagetools inspect postgres:18-alpine`, confirm the annotated PostgreSQL and Alpine versions plus `linux/amd64` and `linux/arm64` manifests, update the versioned tag and identical index digest in `compose.yaml` and `.github/workflows/ci.yml` in one review, then rerun all Task 13 gates. A changed registry digest never enters through an unrelated edit.

## Plan self-review record

Completed on 2026-08-12 against the saved plan after the final security and acceptance review. The record below contains only checks that were actually executed.

- Go-block evidence: 68 Go fences were found. All 48 complete blocks beginning with `package` parsed through `gofmt`; failures were zero. A static qualified-name import-use scan checked 283 non-blank/non-dot imports; failures were zero. These are syntax and static import-use results, not compilation evidence.
- Shell, YAML, fence, and SQL evidence: all 67 Bash fences passed `/bin/bash -n`; both YAML fences parsed; all 300 Markdown fence lines were balanced. Fifty-five Go raw SQL strings contained numbered parameters, and all 55 used contiguous `$1..$n` sets.
- Cold-start and cleanup evidence: all 11 executable Compose start lines use exact `docker compose up -d --wait --wait-timeout 60 postgres`; plain immediate `docker compose up -d postgres` command lines were zero. Task 13 contains seven disposable-database drop traps; all seven record cleanup failure, promote an otherwise successful block to nonzero, preserve an earlier nonzero status, and contain no fail-open drop fallback. The ten operational cleanup scripts plus the focused signal-check cleanup produced 11 exact EXIT, INT-to-130, TERM-to-143, and disable-all registrations; legacy combined registrations were zero. The focused signal command actually returned 130 for INT and 143 for TERM.
- Server-ownership evidence: the Task 12 local smoke block and the two Task 13 server blocks each use `kill -0`, exact spawned-PID `lsof` LISTEN ownership, and a bounded startup ownership wait. Seven pre-curl guards and three post-request guards were found across those three scripts.
- Canonical-secret evidence: one expected-length/strict/round-trip decoder has seven parser call sites covering encryption-key material, bearer locator and secret, claim secret, and recovery locator and secret. Six CR and six LF test cases cover those formats. The only direct `RawURLEncoding.Strict().DecodeString` call is inside that helper.
- Argon2 admission evidence: the complete `argon2.go` block contains one exported concrete `RecoveryPermit` handle with exactly one field (`state *recoveryPermitState`), one unexported shared-state type, zero permit interfaces, zero `permitState` helpers/accessors, exact `*RecoveryPermit` acquire/derive signatures, and exactly one `argon2.IDKey` call. Five focused permit test declarations cover nil/zero rejection, copied-handle shared state and single release, same-handle derivation serialization, release waiting for active derivation, and sequential generation/verification; the process-capacity test and third-recovery cancellation-before-database test remain present.
- Gitleaks execution evidence: a disposable `git clone --no-local` clone was clean and contained no checked-out local secret path; only that clone was mounted read-only into the digest-pinned Alpine container. The exact portable `sha256sum -c` command printed `/tmp/gitleaks.tar.gz: OK`, and the redacted full-history Gitleaks scan exited zero without exposing match content.
- Final static boundary evidence: the word-family placeholder scan returned zero matches without treating normal Go variadic syntax as a placeholder. Unsafe working-tree scanner mounts, fail-open disposable-database drops, plain non-waiting Compose starts, and mutable external runtime-image invocations returned zero matches. One Task 1 assertion checks the exact current `HEAD` documentation subject, and one final-gate assertion checks the same subject at `HEAD~13`.
- Whitespace and workspace evidence: `git diff --check` exited zero. `git diff --no-index --check /dev/null docs/superpowers/plans/2026-08-12-mcpaste-identity-server.md` returned the expected status 1 because the untracked plan differs from `/dev/null`, with zero `trailing whitespace` or `space before tab` diagnostics. `git status --short --branch` showed `main`, this untracked plan, and the separate untracked `docs/superpowers/records/`; no tracked file change was reported.

These results verify plan text, block syntax, selected static invariants, and the isolated scanner command only. They do not compile or execute the planned Phase 2 implementation, run its module graph, start PostgreSQL, or run its unit/integration tests.
