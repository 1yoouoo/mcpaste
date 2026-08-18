# MCPaste

MCPaste is a macOS menu bar app that deliberately hands plain text and static images to AI coding tools through MCP.

> Status: Phases 1–6 are implemented and deployed to the production MVP. The public macOS release remains gated on the owner's Apple signing/notarization credentials and release-tag action.

## Install

Works on macOS and Linux (`amd64`/`arm64`). The installer downloads the prebuilt `mcpaste` CLI from [GitHub Releases](https://github.com/1yoouoo/mcpaste/releases/latest), verifies its checksum, and installs it to `/usr/local/bin/mcpaste`:

```sh
curl -fsSL https://raw.githubusercontent.com/1yoouoo/mcpaste/main/install.sh | sh
```

On macOS, add `--app` to also install the menu bar app (the full read/write interface) to `/Applications`:

```sh
curl -fsSL https://raw.githubusercontent.com/1yoouoo/mcpaste/main/install.sh | sh -s -- --app
```

Install the app through the terminal, not a browser: current releases are ad-hoc signed (not notarized), which runs cleanly when fetched with `curl` but is blocked by Gatekeeper when downloaded with a browser. Manual downloads and checksum verification are documented in [Releases](docs/releases.md).

The MCPaste service endpoint is baked into the release binary at build time, so `go install github.com/...` does not produce a working binary; build from source with the `-ldflags` recipe under [Development prerequisites](#development-prerequisites) instead.

## Usage

### 1. Pair the machine (once per machine)

```sh
mcpaste setup --name my-machine
```

`setup` prints a pairing code (`short_code=XXXXXXXX`) and waits up to 5 minutes for approval. Approve the code from a device that already has full access, either way:

- **GUI**: MCPaste menu bar icon → Workspace & devices → Approve a device → enter the code.
- **Terminal (macOS only)**: `mcpaste approve <code>`.

On approval, `setup` stores a read-only connector credential in `$XDG_CONFIG_HOME/mcpaste/credential.json` (default `~/.config/mcpaste/credential.json`) and automatically registers `mcpaste` as an MCP server in every AI tool configuration it detects:

- Codex: `~/.codex/config.toml` (override with `--codex-config` or `CODEX_CONFIG_PATH`)
- Claude Code: `~/.claude.json` (override with `--claude-config` or `CLAUDE_CONFIG_PATH`)

If neither configuration exists, `setup` fails with `no Codex or Claude Code configuration detected` — start the AI tool once first, or pass an explicit config path. Restart the AI tool after setup so it picks up the new MCP server.

### 2. Use it from the AI tool

After setup, Codex and Claude Code have an `mcpaste` MCP server exposing one read-only tool, `get_latest_paste`, which returns the most recent paste (text and images) from the workspace. Create and edit pastes in the macOS menu bar app; ask the AI tool to read the latest paste.

`mcpaste` with no arguments runs the read-only STDIO MCP proxy — this is what the AI tools invoke; you normally never run it by hand.

### 3. Approve devices from the terminal (optional, macOS only)

```sh
mcpaste login                 # once; approve the printed code in the MCPaste app
mcpaste approve <short-code>  # from then on, approve any device without the GUI
```

`login` pairs this machine as a full device and stores the admin credential at `~/.config/mcpaste/admin-credential.json` (plaintext file — protect it like a password). The service grants full scope only to macOS, so Linux machines always stay read-only connectors.

### Command reference

| Command | What it does | Flags |
|---|---|---|
| `mcpaste` | Run the read-only STDIO MCP proxy (used by AI tools) | — |
| `mcpaste setup` | Pair as a read-only connector and register with detected AI tools | `--name`, `--credential-file`, `--codex-config`, `--claude-config` |
| `mcpaste login` | Pair as a full admin device (macOS only) | `--name`, `--credential-file` |
| `mcpaste approve <short-code>` | Approve a pending pairing request | `--credential-file` |

Never put tokens, pairing codes, claim secrets, or recovery codes in URLs, logs, screenshots, issues, or pull requests.

## Product boundary

- MCPaste does not automatically monitor the clipboard.
- The full macOS app is the supported create/edit/delete interface.
- Codex, Claude Code, and headless Linux companions are read-only.
- The MCPaste service is the central source of truth.
- Treat data as sensitive by default.

## Trust model

MCPaste is not end-to-end encrypted: the service decrypts data. The approved design plans TLS for data in transit and application-level encryption for stored text bodies and image files, but an operator or a full server compromise can read the data. Retention by downstream AI tools and providers is outside MCPaste's control.

See [Security and secrets](docs/security-and-secrets.md) and the [Security Policy](SECURITY.md).

## Architecture

```text
Full Mac app --HTTPS write/sync--> MCPaste service --> PostgreSQL/files
Codex / Claude Code --STDIO--> mcpaste connector --Streamable HTTP MCP--> MCPaste service
```

## Production MVP

The production MVP is one DigitalOcean Droplet with Caddy, the Go service, and PostgreSQL. The implementation consists of a SwiftUI app, Go service, and Go connector.

Static images are normalized on macOS, encrypted on the server, retained for 24 hours, and returned to the read-only MCP tool as ordered image blocks. Text revisions remain immutable and are retained for one year.

## Development prerequisites

- Go version from `.go-version`
- Docker
- Full Xcode for SwiftPM tests, archive, signing, and notarization

Production setup and rollback are documented in [Operations](docs/operations.md); artifact verification is in [Releases](docs/releases.md).

Start PostgreSQL, initialize the ignored mode-0600 local environment once, migrate, and run the server with:

```sh
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
if [ ! -f .env.local ]; then
  umask 077
  if ! cp .env.example .env.local; then
    printf '%s\n' 'Unable to initialize the local environment.' >&2
    exit 1
  fi
fi
if ! chmod 600 .env.local; then
  printf '%s\n' 'Unable to secure the local environment.' >&2
  exit 1
fi
set -a
source .env.local
set +a
if ! (
  set -u
  mcpaste_env_tmp=''
  mcpaste_key_raw=''
  mcpaste_local_key=''
  mcpaste_bootstrap_cleanup() {
    mcpaste_bootstrap_status=$?
    trap - EXIT HUP INT TERM
    if [ -n "$mcpaste_env_tmp" ]; then
      rm -f "$mcpaste_env_tmp"
    fi
    unset mcpaste_env_tmp mcpaste_key_raw mcpaste_local_key
    exit "$mcpaste_bootstrap_status"
  }
  trap mcpaste_bootstrap_cleanup EXIT
  trap 'exit 1' HUP INT TERM

  mcpaste_key_status=0
  grep -q '^MCPASTE_ENCRYPTION_KEYS=' .env.local || mcpaste_key_status=$?
  case "$mcpaste_key_status" in
    0) exit 0 ;;
    1) ;;
    *) exit 1 ;;
  esac
  if ! mcpaste_key_raw="$(openssl rand -base64 32)"; then
    exit 1
  fi
  if ! mcpaste_local_key="$(printf '%s' "$mcpaste_key_raw" | tr '+/' '-_')"; then
    exit 1
  fi
  mcpaste_local_key="${mcpaste_local_key%=}"
  if [ "${#mcpaste_local_key}" -ne 43 ]; then
    exit 1
  fi
  case "$mcpaste_local_key" in
    *[!A-Za-z0-9_-]*) exit 1 ;;
  esac
  if ! mcpaste_env_tmp="$(mktemp ./.env.local.tmp.XXXXXX)"; then
    exit 1
  fi
  if ! chmod 600 "$mcpaste_env_tmp"; then
    exit 1
  fi
  if ! cp .env.local "$mcpaste_env_tmp"; then
    exit 1
  fi
  if ! chmod 600 "$mcpaste_env_tmp"; then
    exit 1
  fi
  if ! printf '\nMCPASTE_ENCRYPTION_KEYS=%s:%s\n' "$MCPASTE_ACTIVE_KEY_ID" "$mcpaste_local_key" >>"$mcpaste_env_tmp"; then
    exit 1
  fi
  if ! mv -f "$mcpaste_env_tmp" .env.local; then
    exit 1
  fi
  mcpaste_env_tmp=''
); then
  printf '%s\n' 'Unable to initialize the local encryption keyring.' >&2
  exit 1
fi
set -a
source .env.local
set +a
go run ./cmd/migrate up
go test ./cmd/migrate ./cmd/mcpaste ./cmd/server ./db/migrations ./internal/config ./internal/connector ./internal/database ./internal/database/migrate ./internal/httpserver ./internal/identity ./internal/identity/postgres ./internal/mcpserver ./internal/secure ./internal/testdb
go run ./cmd/server
```

The listener check is read-only. If port 55439 is occupied, stop setup and identify the owner; never stop or reconfigure an unrelated process or container. `docker compose up -d --wait --wait-timeout 60 postgres` waits for the defined healthcheck and fails after 60 seconds instead of racing a cold database. On later sessions, run `set -a; source .env.local; set +a`; the bootstrap keeps all existing non-sensitive variables and never replaces an existing `MCPASTE_ENCRYPTION_KEYS` line. Keep every old key ID and key value while the PostgreSQL volume contains encrypted replay rows. For an intentional rotation, add a newly generated key under a new ID, retain all old `id:key` entries in `MCPASTE_ENCRYPTION_KEYS`, and change `MCPASTE_ACTIVE_KEY_ID` to the new ID. Never reuse an old ID with new bytes. If the retained key is lost or a disposable local reset is preferred, run `docker compose down --volumes`, remove `.env.local`, and rerun the bootstrap; the volume deletion is destructive.

`go run ./cmd/migrate status` and `go run ./cmd/migrate verify` must report `applied=4 available=4`. `go run ./cmd/migrate down --steps 1` is destructive local rollback and is never part of application rollback. Stop local PostgreSQL without deleting data with `docker compose down`.

Phase 2 exposes anonymous workspace, pairing, recovery, and full-device administration under `/v1/`. Authorization and idempotency values use headers. Pairing claim and recovery values use JSON request bodies. Never put tokens, pairing codes, claim secrets, recovery codes, or QR payloads in a URL, command history, log, screenshot, issue, or pull request.

Phase 3 exposes text synchronization under the same versioned API. Full credentials may create, revise, tombstone, list, and sync text pastes; connector credentials are rejected on every write and sync route. `GET /v1/sync?after=<sequence>&limit=<n>` returns ordered durable events and a cursor, while `GET /v1/events?after=<sequence>` sends metadata-only SSE invalidations. Clients should poll `/v1/sync` every 15 seconds when SSE is unavailable. Text bodies are preserved exactly, encrypted at rest, retained for one year, and removed by the cleanup worker after expiry.

The read-only MCP endpoint exposes only `get_latest_paste`. Run `mcpaste setup --name linux-companion` after an administrator approves the printed pairing request; the service endpoint is selected at build time through `MCPASTE_ENDPOINT`, setup stores the connector credential in `$XDG_CONFIG_HOME/mcpaste/credential.json` (or `~/.config/mcpaste/credential.json`), and detected Codex and Claude Code configurations contain no endpoint override or token. With no arguments, `mcpaste` runs the read-only STDIO proxy. The connector never exposes write APIs or paste bodies in logs.

For a local contributor build, configure the official service endpoint without placing it in application source:

```sh
export MCPASTE_ENDPOINT='https://<official-service-host>'
./scripts/configure-endpoint.sh
(cd macos/MCPaste && swift test && swift build -c release)
go build -ldflags "-X=github.com/1yoouoo/mcpaste/internal/connector.BuildEndpoint=$MCPASTE_ENDPOINT" ./cmd/mcpaste
```

Never use real secrets in environment files, fixtures, logs, screenshots, issues, or pull requests. Use visibly fake deterministic examples such as `example-token-not-real`.

Public operational details are in [Operations](docs/operations.md), [Releases](docs/releases.md), and [Security and secrets](docs/security-and-secrets.md).
