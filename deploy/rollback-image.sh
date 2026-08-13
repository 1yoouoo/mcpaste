#!/usr/bin/env bash
set -euo pipefail

compose_file="${MCPASTE_COMPOSE_FILE:-/opt/mcpaste/deploy/compose.production.yaml}"
deploy_env="${MCPASTE_DEPLOY_ENV:-/etc/mcpaste/deploy.env}"
previous_file="${deploy_env}.previous"
test -r "$compose_file"
test -r "$previous_file"
previous="$(head -n 1 "$previous_file")"
case "$previous" in
    MCPASTE_IMAGE=*@sha256:*) ;;
    *) printf '%s\n' 'Previous deployment is not an immutable image.' >&2; exit 1 ;;
esac
install -m 0600 "$previous_file" "$deploy_env"
docker compose --env-file "$deploy_env" -f "$compose_file" up -d server
printf '%s\n' 'Application image rollback requested; database down migrations were not run.'
