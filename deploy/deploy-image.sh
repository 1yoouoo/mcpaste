#!/usr/bin/env bash
set -euo pipefail

image="${1:?usage: deploy-image.sh ghcr.io/owner/mcpaste@sha256:... [--smoke-stdin]}"
case "$image" in
    *@sha256:*) ;;
    *) printf '%s\n' 'Deployment requires an immutable image digest.' >&2; exit 1 ;;
esac
compose_file="${MCPASTE_COMPOSE_FILE:-/opt/mcpaste/deploy/compose.production.yaml}"
deploy_env="${MCPASTE_DEPLOY_ENV:-/etc/mcpaste/deploy.env}"
server_env="${MCPASTE_SERVER_ENV_FILE:-/etc/mcpaste/server.env}"
postgres_env="${MCPASTE_POSTGRES_ENV_FILE:-/etc/mcpaste/postgres.env}"
if [[ "${2:-}" == '--smoke-stdin' ]]; then
	if ! IFS= read -r endpoint || [[ -z "$endpoint" ]]; then
		printf '%s\n' 'MCPASTE_HEALTH_ENDPOINT is required on stdin.' >&2
		exit 1
	fi
	if ! IFS= read -r smoke_token || [[ -z "$smoke_token" ]]; then
		printf '%s\n' 'MCPASTE_SMOKE_TOKEN is required on stdin.' >&2
		exit 1
	fi
else
	endpoint="${MCPASTE_HEALTH_ENDPOINT:?MCPASTE_HEALTH_ENDPOINT is required}"
	smoke_token="${MCPASTE_SMOKE_TOKEN:?MCPASTE_SMOKE_TOKEN is required}"
fi
script_dir="$(cd "$(dirname "$0")" && pwd)"
test -r "$compose_file"
test -r "$server_env"
test -r "$postgres_env"
install -d -m 0700 "$(dirname "$deploy_env")"
previous=''
if [[ -f "$deploy_env" ]]; then
	previous="$(awk -F= '$1 == "MCPASTE_IMAGE" {print substr($0, index($0, "=") + 1); exit}' "$deploy_env")"
fi
if [[ -n "$previous" ]]; then cp "$deploy_env" "${deploy_env}.previous"; chmod 0600 "${deploy_env}.previous"; fi
restore_previous() {
	if [[ -n "$previous" ]]; then
		cp "${deploy_env}.previous" "$deploy_env"
		chmod 0600 "$deploy_env"
		docker compose --env-file "$deploy_env" -f "$compose_file" up -d server || true
	fi
}
if [[ -f "$deploy_env" ]]; then
	awk -v image="$image" '
		$1 == "MCPASTE_IMAGE" { print "MCPASTE_IMAGE=" image; found = 1; next }
		{ print }
		END { if (!found) print "MCPASTE_IMAGE=" image }
	' "$deploy_env" >"${deploy_env}.next"
else
	printf 'MCPASTE_IMAGE=%s\n' "$image" >"${deploy_env}.next"
fi
chmod 0600 "${deploy_env}.next"
docker compose --env-file "${deploy_env}.next" -f "$compose_file" pull server
docker compose --env-file "${deploy_env}.next" -f "$compose_file" up -d --wait --wait-timeout 60 postgres
docker compose --env-file "${deploy_env}.next" -f "$compose_file" run --rm --no-deps --entrypoint /mcpaste-migrate server up
mv -f "${deploy_env}.next" "$deploy_env"
if ! docker compose --env-file "$deploy_env" -f "$compose_file" up -d server caddy; then
	restore_previous
	exit 1
fi
if ! MCPASTE_SMOKE_TOKEN="$smoke_token" "$script_dir/health-smoke.sh" "$endpoint"; then
	restore_previous
	exit 1
fi
printf '%s\n' 'Deployment passed readiness and smoke checks.'
