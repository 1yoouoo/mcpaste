#!/usr/bin/env bash
set -euo pipefail

endpoint="${1:?usage: health-smoke.sh https://host}"
curl --fail --silent --show-error --max-time 10 "$endpoint/livez" >/dev/null
curl --fail --silent --show-error --max-time 10 "$endpoint/readyz" >/dev/null
if [[ -n "${MCPASTE_SMOKE_TOKEN:-}" ]]; then
	curl --fail --silent --show-error --max-time 10 --request POST \
		-H "Authorization: Bearer ${MCPASTE_SMOKE_TOKEN}" \
		-H 'Accept: application/json, text/event-stream' \
		-H 'Content-Type: application/json' \
		--data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpaste-health","version":"1"}}}' \
		"$endpoint/v1/mcp" >/dev/null
fi
