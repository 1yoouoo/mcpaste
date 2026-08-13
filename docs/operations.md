# MCPaste production operations

This runbook describes the approved single-host topology: Caddy is public on ports 80/443, the Go service and PostgreSQL are private, and image data lives on the persistent `mcpaste-data` volume. The design intentionally has no application backup; losing the host volume loses retained paste data.

## Owner prerequisites

- Ubuntu host with Docker Engine and Compose v2, a reserved public address, and a firewall allowing only TCP 80 and 443.
- A DNS A/AAAA record for the production hostname, with the hostname used as `MCPASTE_DOMAIN`.
- A protected GitHub `production` environment containing the deploy host, user, SSH key, known-hosts, health endpoint, and MCP connector smoke token (`MCPASTE_SMOKE_TOKEN`) values.
- `/etc/mcpaste/server.env`, owned by `root:root`, mode `0600`, populated from `deploy/server.env.example` with the database URL and 32-byte raw URL-base64 AES key.
- `/etc/mcpaste/postgres.env`, owned by `root:root`, mode `0600`, populated from `deploy/postgres.env.example` with only the PostgreSQL bootstrap variables. Never place application encryption keys in this file.
- `/etc/mcpaste/deploy.env`, mode `0600`, containing only immutable `MCPASTE_IMAGE` and `MCPASTE_DOMAIN` values for Compose interpolation.

## First boot

```sh
sudo install -d -o root -g root -m 0700 /etc/mcpaste
sudo install -o root -g root -m 0600 deploy/server.env.example /etc/mcpaste/server.env
sudo install -o root -g root -m 0600 deploy/postgres.env.example /etc/mcpaste/postgres.env
sudoedit /etc/mcpaste/server.env
sudoedit /etc/mcpaste/postgres.env
sudo sh -c 'printf "%s\n" "MCPASTE_DOMAIN=YOUR_REAL_HOSTNAME" "MCPASTE_IMAGE=ghcr.io/YOUR_OWNER/mcpaste@sha256:REPLACE_WITH_DIGEST" > /etc/mcpaste/deploy.env'
sudo chmod 0600 /etc/mcpaste/deploy.env
sudo deploy/bootstrap-host.sh
sudo docker compose --env-file /etc/mcpaste/deploy.env -f deploy/compose.production.yaml up -d postgres
sudo docker compose --env-file /etc/mcpaste/deploy.env -f deploy/compose.production.yaml run --rm --no-deps server /mcpaste-migrate up
sudo docker compose --env-file /etc/mcpaste/deploy.env -f deploy/compose.production.yaml up -d server caddy
MCPASTE_SMOKE_TOKEN='RETRIEVE_FROM_A_PROTECTED_OPERATOR_CHANNEL' deploy/health-smoke.sh https://YOUR_REAL_HOSTNAME
```

Do not put the smoke token in a file, URL, CI log, or shell history. The first image reference must be a release digest. `docker compose config` must show no public PostgreSQL port before startup.

## Routine deployment

CI builds the tested `main` commit, publishes an immutable GHCR digest, and invokes `deploy-image.sh` over verified SSH. The script runs forward migrations before replacing the application and performs live/readiness smoke checks. A failed health check restores the previous application image automatically. It never runs a database down migration.

## Rollback

```sh
sudo deploy/rollback-image.sh
sudo deploy/health-smoke.sh https://YOUR_REAL_HOSTNAME
```

Rollback changes only the application image. If a migration itself is incompatible, stop and investigate with the database owner; do not run a destructive down migration on production.

## Retention, rotation, and incidents

- Text revisions and tombstones are retained for one year, then purged by the cleanup worker.
- Image bundles expire after 24 hours. Every read path checks expiry, and cleanup removes encrypted files and metadata.
- The cleanup interval must remain between one minute and one hour; production uses one minute.
- Explicit paste deletion removes image files immediately after the tombstone is committed.
- For key rotation, add a new key ID/key pair, retain old pairs needed by existing rows, and change only `MCPASTE_ACTIVE_KEY_ID`. Never reuse an ID with different bytes.
- Revoke exposed credentials first. If the image volume or encryption key is lost, treat encrypted data as unavailable and record the incident. The no-backup decision is an accepted product risk.
