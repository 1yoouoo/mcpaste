#!/usr/bin/env bash
set -euo pipefail

compose_file="${MCPASTE_COMPOSE_FILE:-/opt/mcpaste/deploy/compose.production.yaml}"
deploy_env="${MCPASTE_DEPLOY_ENV:-/etc/mcpaste/deploy.env}"
previous_file="${deploy_env}.previous"
test -r "$compose_file"
test -r "$previous_file"
previous_image="$(awk -F= '$1 == "MCPASTE_IMAGE" {print substr($0, index($0, "=") + 1); exit}' "$previous_file")"
case "$previous_image" in
	*@sha256:*) ;;
	*) printf '%s\n' 'Previous deployment is not an immutable image.' >&2; exit 1 ;;
esac
install -m 0600 "$previous_file" "$deploy_env"
docker compose --env-file "$deploy_env" -f "$compose_file" up -d server
printf '%s\n' 'Application image rollback requested; database down migrations were not run.'
