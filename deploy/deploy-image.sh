#!/usr/bin/env bash
set -euo pipefail

image="${1:?usage: deploy-image.sh ghcr.io/owner/mcpaste@sha256:...}"
case "$image" in
    *@sha256:*) ;;
    *) printf '%s\n' 'Deployment requires an immutable image digest.' >&2; exit 1 ;;
esac
compose_file="${MCPASTE_COMPOSE_FILE:-/opt/mcpaste/deploy/compose.production.yaml}"
deploy_env="${MCPASTE_DEPLOY_ENV:-/etc/mcpaste/deploy.env}"
endpoint="${MCPASTE_HEALTH_ENDPOINT:?MCPASTE_HEALTH_ENDPOINT is required}"
script_dir="$(cd "$(dirname "$0")" && pwd)"
test -r "$compose_file"
test -r /etc/mcpaste/server.env
install -d -m 0700 "$(dirname "$deploy_env")"
previous=''
if [[ -f "$deploy_env" ]]; then
	previous="$(awk -F= '$1 == "MCPASTE_IMAGE" {print substr($0, index($0, "=") + 1); exit}' "$deploy_env")"
fi
if [[ -n "$previous" ]]; then printf '%s\n' "$previous" >"${deploy_env}.previous"; fi
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
docker compose --env-file "${deploy_env}.next" -f "$compose_file" run --rm --no-deps server /mcpaste-migrate up
mv -f "${deploy_env}.next" "$deploy_env"
docker compose --env-file "$deploy_env" -f "$compose_file" up -d server
if ! MCPASTE_SMOKE_TOKEN="${MCPASTE_SMOKE_TOKEN:-}" "$script_dir/health-smoke.sh" "$endpoint"; then
    if [[ -n "$previous" ]]; then
        printf 'MCPASTE_IMAGE=%s\n' "$previous" >"${deploy_env}.failed"
        chmod 0600 "${deploy_env}.failed"
        mv -f "${deploy_env}.failed" "$deploy_env"
        docker compose --env-file "$deploy_env" -f "$compose_file" up -d server
    fi
    exit 1
fi
printf '%s\n' 'Deployment passed readiness and smoke checks.'
