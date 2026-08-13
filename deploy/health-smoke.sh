#!/usr/bin/env bash
set -euo pipefail

endpoint="${1:?usage: health-smoke.sh https://host}"
curl --fail --silent --show-error --max-time 10 "$endpoint/livez" >/dev/null
curl --fail --silent --show-error --max-time 10 "$endpoint/readyz" >/dev/null
if [[ -n "${MCPASTE_SMOKE_TOKEN:-}" ]]; then
    curl --fail --silent --show-error --max-time 10 \
        -H "Authorization: Bearer ${MCPASTE_SMOKE_TOKEN}" \
        -H 'Accept: application/json' \
        "$endpoint/v1/mcp" >/dev/null
fi
