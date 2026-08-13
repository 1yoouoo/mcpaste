#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
for workflow in "$root"/.github/workflows/*.yml; do
    rg -q '^permissions:' "$workflow"
    if rg -n 'uses: [^[:space:]]+@(v|main|master|latest)' "$workflow"; then
        printf '%s\n' 'Workflow action is not pinned to an immutable revision.' >&2
        exit 1
    fi
done
! rg -n 'MCPASTE_(ENCRYPTION_KEYS|DATABASE_URL)=|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY' "$root/.github/workflows" "$root/deploy" --glob '!server.env.example'
rg -q 'MCPASTE_DEPLOY_KNOWN_HOSTS' "$root/.github/workflows/deploy.yml"
rg -q 'APPLE_NOTARIZATION_APPLE_ID' "$root/.github/workflows/macos-release.yml"
