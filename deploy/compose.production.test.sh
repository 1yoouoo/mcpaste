#!/usr/bin/env bash
set -euo pipefail

compose_file="$(cd "$(dirname "$0")" && pwd)/compose.production.yaml"
test -f "$compose_file"
test -f "$(dirname "$compose_file")/Caddyfile"
test -f "$(dirname "$compose_file")/postgres.env.example"
grep -Eq '80:80' "$compose_file"
grep -Eq '443:443' "$compose_file"
! grep -Eq '5432:' "$compose_file"
grep -Eq 'internal: true' "$compose_file"
grep -Eq 'mcpaste-postgres:/var/lib/postgresql$' "$compose_file"
grep -Eq 'MCPASTE_POSTGRES_ENV_FILE:-/etc/mcpaste/postgres.env' "$compose_file"
grep -Eq 'mcpaste-data:/var/lib/mcpaste/data' "$compose_file"
grep -Eq 'condition: service_completed_successfully' "$compose_file"
grep -Eq 'chown 65532:65532' "$compose_file"
grep -Eq 'USER 65532:65532|user: 65532:65532' Dockerfile "$compose_file"
grep -Eq 'MCPASTE_DATABASE_URL|MCPASTE_ENCRYPTION_KEYS' "$(dirname "$compose_file")/server.env.example"
! grep -Eq 'MCPASTE_ENCRYPTION_KEYS' "$(dirname "$compose_file")/postgres.env.example"

temporary_dir="$(mktemp -d)"
cleanup() {
    find "$temporary_dir" -type f -delete
    rmdir "$temporary_dir"
}
trap cleanup EXIT
cp "$(dirname "$compose_file")/server.env.example" "$temporary_dir/server.env"
cp "$(dirname "$compose_file")/postgres.env.example" "$temporary_dir/postgres.env"
cat > "$temporary_dir/compose.env" <<EOF
MCPASTE_DOMAIN=example.invalid
MCPASTE_IMAGE=ghcr.io/example/mcpaste@sha256:0000000000000000000000000000000000000000000000000000000000000000
MCPASTE_SERVER_ENV_FILE=$temporary_dir/server.env
MCPASTE_POSTGRES_ENV_FILE=$temporary_dir/postgres.env
EOF
docker compose --env-file "$temporary_dir/compose.env" -f "$compose_file" config --quiet
