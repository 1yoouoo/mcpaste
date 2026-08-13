#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
for workflow in "$root"/.github/workflows/*.yml; do
    grep -Eq '^permissions:' "$workflow"
    if grep -Enq 'uses: [^[:space:]]+@(v|main|master|latest)' "$workflow"; then
        printf '%s\n' 'Workflow action is not pinned to an immutable revision.' >&2
        exit 1
    fi
done
while IFS= read -r file; do
    if grep -Enq 'MCPASTE_(ENCRYPTION_KEYS|DATABASE_URL)=|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY' "$file"; then
        printf '%s\n' 'Workflow or deployment file contains a prohibited secret pattern.' >&2
        exit 1
    fi
done < <(find "$root/.github/workflows" "$root/deploy" -type f ! -name 'server.env.example' -print)
grep -Eq 'MCPASTE_DEPLOY_KNOWN_HOSTS' "$root/.github/workflows/deploy.yml"
grep -Eq 'APPLE_NOTARIZATION_APPLE_ID' "$root/.github/workflows/macos-release.yml"
grep -Eq '^  workflow_call:' "$root/.github/workflows/macos-release.yml"
grep -Eq '^      tag:' "$root/.github/workflows/macos-release.yml"
grep -Eq 'gh release upload .*--clobber' "$root/.github/workflows/macos-release.yml"
grep -Eq '^    needs: linux$' "$root/.github/workflows/release.yml"
grep -Eq 'uses: \.\/\.github\/workflows\/macos-release\.yml' "$root/.github/workflows/release.yml"
