#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
for script in bootstrap-host.sh deploy-image.sh rollback-image.sh health-smoke.sh; do
    bash -n "$root/deploy/$script"
done
! grep -Eq 'migrate down|down --steps' "$root/deploy/deploy-image.sh" "$root/deploy/rollback-image.sh"
grep -Eq 'sha256' "$root/deploy/deploy-image.sh" "$root/deploy/rollback-image.sh"
grep -Eq 'cp.*previous' "$root/deploy/deploy-image.sh"
grep -Eq 'MCPASTE_SMOKE_TOKEN.*required' "$root/deploy/deploy-image.sh"
grep -Eq 'MCPASTE_SMOKE_TOKEN' "$root/deploy/health-smoke.sh"
grep -Eq 'for attempt in \{1\.\.12\}' "$root/deploy/health-smoke.sh"
grep -Eq 'sleep 5' "$root/deploy/health-smoke.sh"
grep -Eq -- '--smoke-stdin' "$root/deploy/deploy-image.sh" "$root/.github/workflows/deploy.yml"
grep -Eq 'read -r endpoint' "$root/deploy/deploy-image.sh"
grep -Eq 'MCPASTE_POSTGRES_ENV_FILE.*postgres.env' "$root/deploy/deploy-image.sh"
grep -Eq 'MCPASTE_POSTGRES_ENV_FILE:-/etc/mcpaste/postgres.env' "$root/deploy/compose.production.yaml"
grep -Eq 'curl.*livez.*\|\| return 1' "$root/deploy/health-smoke.sh"
postgres_line="$(grep -n 'docker compose.*up -d --wait --wait-timeout 60 postgres' "$root/deploy/deploy-image.sh" | cut -d: -f1 | head -n1 || true)"
migrate_line="$(grep -n 'docker compose.*run --rm --no-deps --entrypoint /mcpaste-migrate server up' "$root/deploy/deploy-image.sh" | cut -d: -f1 | head -n1 || true)"
if [[ -z "$postgres_line" || -z "$migrate_line" || "$postgres_line" -ge "$migrate_line" ]]; then
    exit 1
fi
grep -Eq 'docker compose.*up -d server caddy' "$root/deploy/deploy-image.sh"
grep -Eq 'mcpaste-postgres:/var/lib/postgresql$' "$root/deploy/compose.production.yaml"
! grep -Eq 'mcpaste-postgres:/var/lib/postgresql/data$' "$root/deploy/compose.production.yaml"
