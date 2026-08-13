#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
for script in bootstrap-host.sh deploy-image.sh rollback-image.sh health-smoke.sh; do
    bash -n "$root/deploy/$script"
done
! rg -q 'migrate down|down --steps' "$root/deploy/deploy-image.sh" "$root/deploy/rollback-image.sh"
rg -q 'sha256' "$root/deploy/deploy-image.sh" "$root/deploy/rollback-image.sh"
rg -q 'cp.*previous' "$root/deploy/deploy-image.sh"
rg -q 'MCPASTE_SMOKE_TOKEN.*required' "$root/deploy/deploy-image.sh"
rg -q 'MCPASTE_SMOKE_TOKEN' "$root/deploy/health-smoke.sh"
rg -q -- '--smoke-stdin' "$root/deploy/deploy-image.sh" "$root/.github/workflows/deploy.yml"
rg -q 'read -r endpoint' "$root/deploy/deploy-image.sh"
