# Security and secrets management

MCPaste is a central server, not an end-to-end encrypted system. Paste text and images are sensitive application data: they are never configuration, credentials, or logging material.

## Data placement

| Data | Approved placement | Never place it in |
| --- | --- | --- |
| Paste text/images | App cache locally; encrypted production database and image volume in production | Git, environment files, logs, screenshots, issues, or pull requests |
| Full Mac credentials | macOS Keychain | App bundle, repository, user defaults, or logs |
| Linux connector credential | System credential store or a user-owned file with mode `0600` | Shell history, URLs, repository, or world-readable configuration |
| Production encryption key | `/etc/mcpaste/server.env`, root-owned and mode `0600` | GitHub Actions, container/image contents, app, connector, or repository |
| Database/session secrets | The same `/etc/mcpaste/server.env` file | Public Compose configuration, CI logs, or clients |
| Deployment SSH key | Protected GitHub production environment | Repository, Droplet image, or pull-request jobs |
| Apple signing secrets | Keychain or protected release secrets | Repository, build logs, or app resources |

## Local development

Copy the safe template before starting local development:

```sh
cp .env.example .env.local
set -a
source .env.local
set +a
```

The foundation settings in `.env.example` are non-sensitive: environment name, HTTP bind address, and log level. The server reads the process environment and does not automatically load `.env.local` or any other dotenv file. Later local credentials must be isolated to the local credential store or ignored local files and must not be reused in production. Never put paste samples, paste content, or real credentials in an environment file.

## Phase 2 identity material

Production receives `MCPASTE_DATABASE_URL`, `MCPASTE_ACTIVE_KEY_ID`, and `MCPASTE_ENCRYPTION_KEYS` through the server process environment populated from `/etc/mcpaste/server.env`. `MCPASTE_ENCRYPTION_KEYS` is a comma-separated keyring of raw URL-base64 AES-256 keys. The active key identifier is stored with each ciphertext; old keys remain in the process keyring only while retained ciphertext still references them. The service never reads an encryption key from a repository file, command argument, request, database row, or client.

Bearer credentials and private pairing claim secrets contain 256 random bits. PostgreSQL stores only domain-separated SHA-256 hashes and non-secret lookup locators. Recovery codes contain 256 random bits and use a workspace UUID plus non-secret locator for indexed lookup; PostgreSQL stores a random salt and Argon2id verifier, never the code. Recovery rotation replaces the verifier in the same transaction that adds the recovered full Mac.

Credential-bearing workspace and recovery responses are encrypted for 24-hour idempotent replay. Approved pairing grants are encrypted for five-minute, byte-identical claim replay. The encrypted rows remain server-sensitive because the running service can decrypt them. Cleanup purges expired replay state and revokes approved devices whose pairing grants expire without a successful claim.

Initialize the ignored local keyring only when it is absent:

```sh
if [ ! -f .env.local ]; then
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
```

The file remains mode 0600 and ignored. Later sessions source it instead of generating another key. Never replace key bytes under a retained key ID while the PostgreSQL volume exists: intentionally rotate by adding a new ID/key pair, retaining every old pair needed by ciphertext, and selecting only the new ID for writes. If local encrypted data is disposable, `docker compose down --volumes` plus removal and recreation of `.env.local` resets both sides together. Do not paste a key value into `.env.example`, documentation, tests, shell transcripts, or review comments. Production keys come only from the root-owned server environment file described below.

## Pre-commit check

Inspect what is staged and run a lightweight secret-pattern check:

```sh
git diff --cached
rg -n -e 'sk-[A-Za-z0-9]{12,}' -e 'ghp_[A-Za-z0-9]{12,}' -e 'AKIA[0-9A-Z]{16}' -e 'BEGIN (RSA|OPENSSH|EC) PRIVATE KEY' .
```

This check supplements, and does not prove the absence of, secrets. Regex literals in documentation are examples, not real secrets. Do not add real secrets or paste content to the repository, logs, screenshots, issues, or pull requests.

## Production placement and network boundary

Production requirements are that the server environment live directly on the Droplet at `/etc/mcpaste/server.env`, owned by `root` and mode `0600`; it must not be baked into an image or passed through GitHub Actions. Caddy must be the public entry point. PostgreSQL must have no public port. Authentication values must use headers and never query strings.

## Logging and capture safety

Logging requirements are that allowed fields may include timestamp, request ID, method, route path, status, duration, object ID, size, and count. Logs must exclude all request and response bodies, query strings, authentication headers, cookies, device credentials, recovery codes, pairing codes, QR data, and image bytes. Handlers must avoid capture by construction: do not log request or response bodies, do not serialize credential-bearing requests, and keep diagnostics separate from paste storage.

## Incident response

If a secret or paste content is exposed, rotate or revoke it first, then remove stored copies and investigate the exposure path. History cleanup cannot restore secrecy; assume a committed or logged value may already have been copied.
