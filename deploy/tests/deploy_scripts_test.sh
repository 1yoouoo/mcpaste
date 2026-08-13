#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
for script in bootstrap-host.sh deploy-image.sh rollback-image.sh health-smoke.sh; do
    bash -n "$root/deploy/$script"
done
! rg -q 'migrate down|down --steps' "$root/deploy/deploy-image.sh" "$root/deploy/rollback-image.sh"
rg -q 'sha256' "$root/deploy/deploy-image.sh" "$root/deploy/rollback-image.sh"
rg -q 'MCPASTE_SMOKE_TOKEN' "$root/deploy/health-smoke.sh"
