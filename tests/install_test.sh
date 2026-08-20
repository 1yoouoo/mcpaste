#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/mcpaste-install-test.XXXXXX")
cleanup() {
    if [ -n "${stale_pid:-}" ] && kill -0 "$stale_pid" 2>/dev/null; then
        kill "$stale_pid" 2>/dev/null || true
    fi
    rm -rf "$test_root"
}
trap cleanup EXIT HUP INT TERM

# Keep the RED run safe: the current installer ignores this override and must
# not be allowed to touch the real /Applications directory.
if ! grep -q 'MCPASTE_INSTALL_ROOT' "$repo_root/install.sh"; then
    printf '%s\n' 'FAIL: installer has no isolated install-root support yet.' >&2
    exit 1
fi

release="$test_root/release"
fixture="$test_root/fixture"
stub_dir="$test_root/stubs"
install_root="$test_root/install"
mkdir -p "$release" "$fixture/MCPaste.app/Contents/Helpers" "$stub_dir"
mkdir -p "$install_root/usr/local/bin"
printf '%s\n' 'fixture-helper' > "$fixture/MCPaste.app/Contents/Helpers/mcpaste"
chmod 755 "$fixture/MCPaste.app/Contents/Helpers/mcpaste"
(cd "$fixture" && zip -q -r "$release/MCPaste-adhoc.zip" MCPaste.app)
(cd "$release" && shasum -a 256 MCPaste-adhoc.zip > macOS-SHA256SUMS)

cat > "$stub_dir/osascript" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >> "$MCPASTE_TEST_OSASCRIPT_LOG"
exit 0
STUB
cat > "$stub_dir/lsof" <<'STUB'
#!/bin/sh
exit 1
STUB
cat > "$stub_dir/ps" <<'STUB'
#!/bin/sh
exit 1
STUB
cat > "$stub_dir/pgrep" <<'STUB'
#!/bin/sh
exit 1
STUB
cat > "$stub_dir/sudo" <<'STUB'
#!/bin/sh
exit 99
STUB
chmod 755 "$stub_dir"/*

export MCPASTE_TEST_OSASCRIPT_LOG="$test_root/osascript.log"
export MCPASTE_DOWNLOAD_BASE="file://$release"
export MCPASTE_INSTALL_ROOT="$install_root"
export PATH="$stub_dir:$PATH"
sh "$repo_root/install.sh"

test -f "$install_root/Applications/MCPaste.app/Contents/Helpers/mcpaste"
test -x "$install_root/Applications/MCPaste.app/Contents/Helpers/mcpaste"
test "$(readlink "$install_root/usr/local/bin/mcpaste")" = \
    "$install_root/Applications/MCPaste.app/Contents/Helpers/mcpaste"
grep -q 'com.mcpaste.app' "$MCPASTE_TEST_OSASCRIPT_LOG"

stale_pid=''
stale_stub_dir="$test_root/stale-stubs"
stale_install_root="$test_root/stale-install"
cp -R "$stub_dir" "$stale_stub_dir"
(sleep 30) &
stale_pid=$!
cat > "$stale_stub_dir/lsof" <<STUB
#!/bin/sh
if kill -0 "$stale_pid" 2>/dev/null; then
    printf '%s\\n' "$stale_pid"
fi
STUB
cat > "$stale_stub_dir/ps" <<'STUB'
#!/bin/sh
printf '%s\n' '/tmp/MCPaste.app/Contents/Helpers/mcpaste peer --port 38421'
STUB
chmod 755 "$stale_stub_dir/lsof" "$stale_stub_dir/ps"
MCPASTE_INSTALL_ROOT="$stale_install_root" \
PATH="$stale_stub_dir:$PATH" \
sh "$repo_root/install.sh" >/dev/null
if kill -0 "$stale_pid" 2>/dev/null; then
    printf '%s\n' 'FAIL: stale peer listener was not terminated.' >&2
    exit 1
fi
stale_pid=''

broken_stub_dir="$test_root/broken-stubs"
broken_install_root="$test_root/broken-install"
cp -R "$stub_dir" "$broken_stub_dir"
cat > "$broken_stub_dir/ln" <<'STUB'
#!/bin/sh
exit 77
STUB
chmod 755 "$broken_stub_dir/ln"

MCPASTE_INSTALL_ROOT="$broken_install_root" \
PATH="$broken_stub_dir:$PATH" \
sh "$repo_root/install.sh" >/dev/null 2>"$test_root/noninteractive-link.log"
test -f "$broken_install_root/Applications/MCPaste.app/Contents/Helpers/mcpaste"
test ! -e "$broken_install_root/usr/local/bin/mcpaste"
grep -q 'skipped the optional' "$test_root/noninteractive-link.log"

printf '%s\n' 'install_test: PASS'
