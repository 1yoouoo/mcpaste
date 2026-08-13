#!/usr/bin/env bash
set -euo pipefail

compose_file="$(cd "$(dirname "$0")" && pwd)/compose.production.yaml"
test -f "$compose_file"
test -f "$(dirname "$compose_file")/Caddyfile"
rg -q '80:80' "$compose_file"
rg -q '443:443' "$compose_file"
! rg -q '5432:' "$compose_file"
rg -q 'internal: true' "$compose_file"
rg -q 'mcpaste-postgres:/var/lib/postgresql$' "$compose_file"
rg -q 'mcpaste-data:/var/lib/mcpaste/data' "$compose_file"
rg -q 'condition: service_completed_successfully' "$compose_file"
rg -q 'chown 65532:65532' "$compose_file"
rg -q 'USER 65532:65532|user: 65532:65532' Dockerfile "$compose_file"
rg -q 'MCPASTE_DATABASE_URL|MCPASTE_ENCRYPTION_KEYS' "$(dirname "$compose_file")/server.env.example"
