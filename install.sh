#!/bin/sh
# Install the mcpaste CLI (and optionally the macOS app) from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/1yoouoo/mcpaste/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/1yoouoo/mcpaste/main/install.sh | sh -s -- --app
#
# --app additionally installs MCPaste.app to /Applications (macOS only).
# Every download is verified against the release's SHA256SUMS before install.
set -eu

base="${MCPASTE_DOWNLOAD_BASE:-https://github.com/1yoouoo/mcpaste/releases/latest/download}"

install_app=false
for argument in "$@"; do
  case "$argument" in
    --app) install_app=true ;;
    *) printf '%s\n' "unknown option: $argument (supported: --app)" >&2; exit 1 ;;
  esac
done

os="$(uname -s)"
arch="$(uname -m)"
case "$arch" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) printf '%s\n' "unsupported architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in
  Darwin) asset="mcpaste-darwin-$arch" sums="Darwin-SHA256SUMS" ;;
  Linux) asset="mcpaste-$arch" sums="Linux-SHA256SUMS" ;;
  *) printf '%s\n' "unsupported operating system: $os" >&2; exit 1 ;;
esac
if [ "$install_app" = true ] && [ "$os" != Darwin ]; then
  printf '%s\n' '--app requires macOS' >&2
  exit 1
fi

checker=sha256sum
command -v sha256sum >/dev/null 2>&1 || checker='shasum -a 256'

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

printf '%s\n' "Downloading $asset..."
curl -fsSL -o "$workdir/$asset" "$base/$asset"
curl -fsSL -o "$workdir/$sums" "$base/$sums"
(cd "$workdir" && grep " $asset\$" "$sums" | $checker -c -)

chmod +x "$workdir/$asset"
destination=/usr/local/bin/mcpaste
if [ -w /usr/local/bin ]; then
  mv "$workdir/$asset" "$destination"
else
  printf '%s\n' "Installing to $destination (sudo may ask for your password)"
  sudo mv "$workdir/$asset" "$destination"
fi
printf '%s\n' "Installed $destination"

if [ "$install_app" = true ]; then
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
fi

printf '%s\n' 'Next: pair this machine with `mcpaste setup --name <name>`.'
