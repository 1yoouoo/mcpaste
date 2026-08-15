#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
script="$root/scripts/configure-endpoint.sh"
output="macos/MCPaste/Sources/MCPasteCore/EndpointConfiguration.generated.swift"

bash -n "$script"

workspace="$(mktemp -d)"
trap 'rm -rf "$workspace"' EXIT
mkdir -p "$workspace/scripts" "$workspace/macos/MCPaste/Sources/MCPasteCore"
cp "$script" "$workspace/scripts/configure-endpoint.sh"

# The workflows call the script from macos/MCPaste, so the generated file has to land in
# the package regardless of the working directory it is invoked from.
(cd "$workspace/macos/MCPaste" && MCPASTE_ENDPOINT='https://mcpaste.example.com' ../../scripts/configure-endpoint.sh)
test -f "$workspace/$output"
grep -Fq 'static let endpoint = "https://mcpaste.example.com"' "$workspace/$output"
test ! -e "$workspace/macos/MCPaste/macos"

rm -f "$workspace/$output"
(cd "$workspace" && MCPASTE_ENDPOINT='https://mcpaste.example.com' ./scripts/configure-endpoint.sh)
test -f "$workspace/$output"

# An endpoint that is missing, plaintext, or carries a path must be refused.
for candidate in '' 'http://mcpaste.example.com' 'https://mcpaste.example.com/v1' 'https://user@mcpaste.example.com'; do
    if (cd "$workspace" && MCPASTE_ENDPOINT="$candidate" ./scripts/configure-endpoint.sh) 2>/dev/null; then
        printf 'configure-endpoint accepted an invalid endpoint: %s\n' "$candidate" >&2
        exit 1
    fi
done

# Both workflows invoke it from the package directory; keep that contract visible here.
grep -Eq '\.\./\.\./scripts/configure-endpoint\.sh' "$root/.github/workflows/ci.yml"
grep -Eq '\.\./\.\./scripts/configure-endpoint\.sh' "$root/.github/workflows/macos-release.yml"
