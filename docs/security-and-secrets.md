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
```

The foundation settings in `.env.example` are non-sensitive: environment name, HTTP bind address, and log level. Later local credentials must be isolated to the local credential store or ignored local files and must not be reused in production. Never put paste samples, paste content, or real credentials in an environment file.

## Pre-commit check

Inspect what is staged and run a lightweight secret-pattern check:

```sh
git diff --cached
rg -n -I -e 'sk-[A-Za-z0-9]' -e 'ghp_[A-Za-z0-9]' -e 'AKIA[0-9A-Z]{16}' -e 'BEGIN (RSA|OPENSSH|EC) PRIVATE KEY' .
```

This check supplements, and does not prove the absence of, secrets. Regex literals in documentation are examples, not real secrets. Do not add real secrets or paste content to the repository, logs, screenshots, issues, or pull requests.

## Production placement and network boundary

The production server environment is written directly on the Droplet at `/etc/mcpaste/server.env`, owned by `root` and mode `0600`. It must not be baked into an image or passed through GitHub Actions. Caddy is the public-facing entry point; PostgreSQL has no public port. Authentication headers are accepted as headers and never as query strings.

## Logging and capture safety

Allowed operational metadata is limited to timestamp, request ID, method, route path, status, duration, object ID, size, and count. Logs explicitly exclude all request and response bodies, query strings, authentication headers, cookies, device credentials, recovery codes, pairing codes, QR data, and image bytes. Avoid capture by construction: do not log request or response bodies, do not serialize credential-bearing requests, and keep diagnostics separate from paste storage.

## Incident response

If a secret or paste content is exposed, rotate or revoke it first, then remove stored copies and investigate the exposure path. History cleanup cannot restore secrecy; assume a committed or logged value may already have been copied.
