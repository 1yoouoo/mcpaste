# MCPaste releases

## Linux artifacts

Tagged releases build `mcpaste`, `mcpaste-server`, and `mcpaste-migrate` for Linux `amd64` and `arm64`, plus `Linux-SHA256SUMS` and a generated `THIRD_PARTY_LICENSES.tar.gz`. The same job also builds the CLI-only `mcpaste-darwin-amd64` and `mcpaste-darwin-arm64` (unsigned, verified through `Darwin-SHA256SUMS`; the signed and notarized artifact is the macOS app below):

```sh
sha256sum --check Linux-SHA256SUMS
file mcpaste-amd64 mcpaste-arm64
```

The connector is read-only and must be configured with a connector credential, never a full credential.

## macOS artifact

The macOS release workflow runs Swift tests, archives with Developer ID signing, submits for notarization, staples the result, and publishes a checksum:

```sh
codesign --verify --deep --strict --verbose=2 /Applications/MCPaste.app
spctl --assess --type execute --verbose=4 /Applications/MCPaste.app
shasum -a 256 MCPaste-final.zip
shasum -a 256 --check macOS-SHA256SUMS
```

The macOS release also publishes `macOS-SHA256SUMS` and `THIRD_PARTY_NOTICES.md`.

Apple Developer membership, a Developer ID identity, notarization credentials, and the protected release environment are owner prerequisites. They are never stored in this repository.

The Linux tag workflow publishes the GitHub release first and then invokes the reusable macOS workflow with the same `v*` tag. A manual macOS run also requires an existing `v*` tag; it never uploads artifacts to a branch-named release.

## Release gate

Before a tag is created, run the full local acceptance commands in the current handoff record, review `git diff`, and run the secret scan. A release tag, GitHub release, registry push, or notarization request is an external owner action and is not performed by local development.
