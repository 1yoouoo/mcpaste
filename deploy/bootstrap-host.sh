#!/usr/bin/env bash
set -euo pipefail

if [[ "$(id -u)" != 0 ]]; then
    printf '%s\n' 'Run bootstrap-host.sh as root on the production host.' >&2
    exit 1
fi
for command in docker install chmod chown; do
    command -v "$command" >/dev/null 2>&1 || { printf '%s\n' "Required host command is unavailable." >&2; exit 1; }
done
install -d -o root -g root -m 0700 /etc/mcpaste
if [[ ! -f /etc/mcpaste/server.env || ! -f /etc/mcpaste/postgres.env ]]; then
    printf '%s\n' 'Create /etc/mcpaste/server.env and /etc/mcpaste/postgres.env from the examples before starting the stack.' >&2
    exit 1
fi
chown root:root /etc/mcpaste/server.env
chmod 0600 /etc/mcpaste/server.env
chown root:root /etc/mcpaste/postgres.env
chmod 0600 /etc/mcpaste/postgres.env
install -d -o root -g root -m 0755 /var/lib/mcpaste
if command -v ufw >/dev/null 2>&1; then
    ufw allow 80/tcp >/dev/null
    ufw allow 443/tcp >/dev/null
    ufw deny 5432/tcp >/dev/null || true
fi
printf '%s\n' 'Host prerequisites and protected environment path are ready.'
