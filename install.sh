#!/bin/sh
# Install MCPaste from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/1yoouoo/mcpaste/main/install.sh | sh
#
# macOS: installs MCPaste.app to /Applications and links the embedded CLI to
# /usr/local/bin/mcpaste. The app pairs this Mac and registers the AI tools by
# itself on first launch.
# Linux: installs the mcpaste connector CLI to /usr/local/bin/mcpaste.
# Every download is verified against the release's SHA256SUMS before install.
set -eu

base="${MCPASTE_DOWNLOAD_BASE:-https://github.com/1yoouoo/mcpaste/releases/latest/download}"

for argument in "$@"; do
  case "$argument" in
    --app) ;; # historical flag; the app install is the macOS default now
    *) printf '%s\n' "unknown option: $argument" >&2; exit 1 ;;
  esac
done

os="$(uname -s)"
arch="$(uname -m)"
case "$arch" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) printf '%s\n' "unsupported architecture: $arch" >&2; exit 1 ;;
esac

checker=sha256sum
command -v sha256sum >/dev/null 2>&1 || checker='shasum -a 256'

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

install_darwin() {
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
  printf '%s\n' "Downloading $app_zip..."
  curl -fsSL -o "$workdir/macOS-SHA256SUMS" "$base/macOS-SHA256SUMS"
  (cd "$workdir" && grep " $app_zip\$" macOS-SHA256SUMS | $checker -c -)
  ditto -xk "$workdir/$app_zip" "$workdir/app"
  if [ -w /Applications ]; then
    rm -rf /Applications/MCPaste.app
    mv "$workdir/app/MCPaste.app" /Applications/MCPaste.app
  else
    printf '%s\n' 'Installing to /Applications (sudo may ask for your password)'
    sudo rm -rf /Applications/MCPaste.app
    sudo mv "$workdir/app/MCPaste.app" /Applications/MCPaste.app
  fi
  printf '%s\n' 'Installed /Applications/MCPaste.app'
  cli=/Applications/MCPaste.app/Contents/Helpers/mcpaste
  if [ -x "$cli" ]; then
    if [ -w /usr/local/bin ]; then
      ln -sf "$cli" /usr/local/bin/mcpaste
    else
      printf '%s\n' 'Linking /usr/local/bin/mcpaste (sudo may ask for your password)'
      sudo mkdir -p /usr/local/bin
      sudo ln -sf "$cli" /usr/local/bin/mcpaste
    fi
    printf '%s\n' 'Linked /usr/local/bin/mcpaste'
  fi
  printf '%s\n' 'Next: open MCPaste.app — it pairs this Mac and connects the AI tools.'
}

install_linux() {
  asset="mcpaste-$arch"
  printf '%s\n' "Downloading $asset..."
  curl -fsSL -o "$workdir/$asset" "$base/$asset"
  curl -fsSL -o "$workdir/Linux-SHA256SUMS" "$base/Linux-SHA256SUMS"
  (cd "$workdir" && grep " $asset\$" Linux-SHA256SUMS | $checker -c -)
  chmod +x "$workdir/$asset"
  destination=/usr/local/bin/mcpaste
  if [ -w /usr/local/bin ]; then
    mv "$workdir/$asset" "$destination"
  else
    printf '%s\n' "Installing to $destination (sudo may ask for your password)"
    sudo mv "$workdir/$asset" "$destination"
  fi
  printf '%s\n' "Installed $destination"
  printf '%s\n' 'Next: pair this machine with `mcpaste setup --name <name>`.'
}

case "$os" in
  Darwin) install_darwin ;;
  Linux) install_linux ;;
  *) printf '%s\n' "unsupported operating system: $os" >&2; exit 1 ;;
esac
