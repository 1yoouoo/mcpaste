#!/usr/bin/env bash
set -euo pipefail

endpoint="${1:?usage: health-smoke.sh https://host}"
check_endpoint() {
	curl --fail --silent --max-time 10 "$endpoint/livez" >/dev/null 2>&1 &&
	curl --fail --silent --max-time 10 "$endpoint/readyz" >/dev/null 2>&1
	if [[ -n "${MCPASTE_SMOKE_TOKEN:-}" ]]; then
		curl --fail --silent --max-time 10 --request POST \
			-H "Authorization: Bearer ${MCPASTE_SMOKE_TOKEN}" \
			-H 'Accept: application/json, text/event-stream' \
			-H 'Content-Type: application/json' \
			--data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpaste-health","version":"1"}}}' \
			"$endpoint/v1/mcp" >/dev/null 2>&1
	fi
}

for attempt in {1..12}; do
	if check_endpoint; then
		exit 0
	fi
	if [[ "$attempt" -lt 12 ]]; then
		sleep 5
	fi
done
printf '%s\n' 'Health smoke checks did not pass after bounded retries.' >&2
exit 1
