#!/bin/sh
# Install the MCPaste macOS app from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/1yoouoo/mcpaste/main/install.sh | sh
set -eu

if [ "$#" -ne 0 ]; then
  printf '%s\n' 'install.sh does not accept options.' >&2
  exit 1
fi
if [ "$(uname -s)" != Darwin ]; then
  printf '%s\n' 'MCPaste requires macOS.' >&2
  exit 1
fi

base="${MCPASTE_DOWNLOAD_BASE:-https://github.com/1yoouoo/mcpaste/releases/latest/download}"
install_root="${MCPASTE_INSTALL_ROOT:-}"
if [ -n "$install_root" ]; then
  applications_dir="$install_root/Applications"
  bin_dir="$install_root/usr/local/bin"
else
  applications_dir=/Applications
  bin_dir=/usr/local/bin
fi
app_path="$applications_dir/MCPaste.app"
workdir="$(mktemp -d)"
cleanup() {
  if [ -n "$workdir" ] && [ -d "$workdir" ]; then
    rm -rf "$workdir"
  fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

verify_checksum() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c -
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c -
  else
    printf '%s\n' 'No SHA-256 checksum tool is available.' >&2
    return 1
  fi
}

listener_pid() {
  lsof -tiTCP:38421 -sTCP:LISTEN 2>/dev/null | sed -n '1p'
}

wait_for_install_target_idle() {
  attempts=0
  while [ "$attempts" -lt 50 ]; do
    if ! pgrep -x MCPaste >/dev/null 2>&1 && [ -z "$(listener_pid)" ]; then
      return 0
    fi
    attempts=$((attempts + 1))
    sleep 0.1
  done
  return 1
}

stop_existing_mcpaste() {
  osascript -e 'tell application id "com.mcpaste.app" to quit' >/dev/null 2>&1 || true
  if pgrep -x MCPaste >/dev/null 2>&1; then
    if wait_for_install_target_idle; then
      return 0
    fi
  elif [ -z "$(listener_pid)" ]; then
    return 0
  fi

  pid="$(listener_pid)"
  if [ -n "$pid" ]; then
    command_line="$(ps -p "$pid" -o command= 2>/dev/null || true)"
    case "$command_line" in
      *mcpaste*peer*--port*38421*)
        kill "$pid" 2>/dev/null || true
        attempts=0
        while [ "$attempts" -lt 20 ] && [ -n "$(listener_pid)" ]; do
          attempts=$((attempts + 1))
          sleep 0.1
        done
        if [ -n "$(listener_pid)" ]; then
          kill -KILL "$pid" 2>/dev/null || true
        fi
        ;;
      *)
        printf '%s\n' 'Cannot replace MCPaste while another process owns TCP port 38421.' >&2
        return 1
        ;;
    esac
  fi

  if ! wait_for_install_target_idle; then
    printf '%s\n' 'MCPaste did not stop cleanly. Close it and run the installer again.' >&2
    return 1
  fi
}

app_zip=''
for candidate in MCPaste-final.zip MCPaste-adhoc.zip; do
  if curl -fsSL -o "$workdir/$candidate" "$base/$candidate" 2>/dev/null; then
    app_zip="$candidate"
    break
  fi
done
if [ -z "$app_zip" ]; then
  printf '%s\n' 'The latest release has no macOS app artifact.' >&2
  exit 1
fi

curl -fsSL -o "$workdir/macOS-SHA256SUMS" "$base/macOS-SHA256SUMS"
checksum_line="$(grep " $app_zip\$" "$workdir/macOS-SHA256SUMS")" || {
  printf '%s\n' 'The release checksum does not cover the downloaded app.' >&2
  exit 1
}
if [ "$(printf '%s\n' "$checksum_line" | wc -l | tr -d ' ')" -ne 1 ]; then
  printf '%s\n' 'The release checksum entry is ambiguous.' >&2
  exit 1
fi
(cd "$workdir" && printf '%s\n' "$checksum_line" | verify_checksum)

ditto -xk "$workdir/$app_zip" "$workdir/app"
test -d "$workdir/app/MCPaste.app"
test -x "$workdir/app/MCPaste.app/Contents/Helpers/mcpaste"

stop_existing_mcpaste

if [ -n "$install_root" ]; then
  mkdir -p "$applications_dir" "$bin_dir"
fi

if [ -w "$applications_dir" ]; then
  rm -rf "$app_path"
  mv "$workdir/app/MCPaste.app" "$app_path"
else
  printf 'Installing to %s (sudo may ask for your password)\n' "$app_path"
  sudo rm -rf "$app_path"
  sudo mv "$workdir/app/MCPaste.app" "$app_path"
fi
printf 'Installed %s\n' "$app_path"

helper="$app_path/Contents/Helpers/mcpaste"
linked=0
if [ -w "$bin_dir" ]; then
  if ln -sf "$helper" "$bin_dir/mcpaste"; then
    linked=1
  fi
elif [ -t 0 ] && [ -t 1 ]; then
    printf 'Linking %s (sudo may ask for your password)\n' "$bin_dir/mcpaste"
    sudo mkdir -p "$bin_dir"
    sudo ln -sf "$helper" "$bin_dir/mcpaste"
    linked=1
elif sudo -n true >/dev/null 2>&1 && sudo -n mkdir -p "$bin_dir" >/dev/null 2>&1 && sudo -n ln -sf "$helper" "$bin_dir/mcpaste" >/dev/null 2>&1; then
  linked=1
fi
if [ "$linked" -eq 1 ]; then
  printf 'Linked %s\n' "$bin_dir/mcpaste"
else
  printf 'Installed the app, but skipped the optional %s link because sudo needs an interactive password.\n' "$bin_dir/mcpaste" >&2
  printf 'Run: sudo ln -sf %s %s\n' "$helper" "$bin_dir/mcpaste" >&2
fi
printf '%s\n' 'Next: install Tailscale, open MCPaste, then restart the desired MCP client after registration.'
