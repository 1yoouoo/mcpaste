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
grep -Eq -- '--smoke-stdin' "$root/deploy/deploy-image.sh" "$root/.github/workflows/deploy.yml"
grep -Eq 'read -r endpoint' "$root/deploy/deploy-image.sh"
postgres_line="$(grep -n 'docker compose.*up -d --wait --wait-timeout 60 postgres' "$root/deploy/deploy-image.sh" | cut -d: -f1 | head -n1 || true)"
migrate_line="$(grep -n 'docker compose.*run --rm --no-deps server /mcpaste-migrate up' "$root/deploy/deploy-image.sh" | cut -d: -f1 | head -n1 || true)"
test -n "$postgres_line" && test -n "$migrate_line" && test "$postgres_line" -lt "$migrate_line"
