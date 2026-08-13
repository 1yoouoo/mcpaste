# MCPaste releases

## Linux artifacts

Tagged releases build `mcpaste`, `mcpaste-server`, and `mcpaste-migrate` for Linux `amd64` and `arm64`, plus `SHA256SUMS`:

```sh
sha256sum --check SHA256SUMS
file mcpaste-amd64 mcpaste-arm64
```

The connector is read-only and must be configured with a connector credential, never a full credential.

## macOS artifact

The macOS release workflow runs Swift tests, archives with Developer ID signing, submits for notarization, staples the result, and publishes a checksum:

```sh
codesign --verify --deep --strict --verbose=2 /Applications/MCPaste.app
spctl --assess --type execute --verbose=4 /Applications/MCPaste.app
shasum -a 256 MCPaste-final.zip
```

Apple Developer membership, a Developer ID identity, notarization credentials, and the protected release environment are owner prerequisites. They are never stored in this repository.

## Release gate

Before a tag is created, run the full local acceptance commands in the current handoff record, review `git diff`, and run the secret scan. A release tag, GitHub release, registry push, or notarization request is an external owner action and is not performed by local development.
