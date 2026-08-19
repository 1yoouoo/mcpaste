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

if [ -w /Applications ]; then
  rm -rf /Applications/MCPaste.app
  mv "$workdir/app/MCPaste.app" /Applications/MCPaste.app
else
  printf '%s\n' 'Installing to /Applications (sudo may ask for your password)'
  sudo rm -rf /Applications/MCPaste.app
  sudo mv "$workdir/app/MCPaste.app" /Applications/MCPaste.app
fi
printf '%s\n' 'Installed /Applications/MCPaste.app'

helper=/Applications/MCPaste.app/Contents/Helpers/mcpaste
if [ -w /usr/local/bin ]; then
  ln -sf "$helper" /usr/local/bin/mcpaste
else
  printf '%s\n' 'Linking /usr/local/bin/mcpaste (sudo may ask for your password)'
  sudo mkdir -p /usr/local/bin
  sudo ln -sf "$helper" /usr/local/bin/mcpaste
fi
printf '%s\n' 'Linked /usr/local/bin/mcpaste'
printf '%s\n' 'Next: install Tailscale, open MCPaste, then restart the desired MCP client after registration.'
