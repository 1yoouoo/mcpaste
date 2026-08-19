# MCPaste releases

Tagged `v*` releases publish a universal macOS app for arm64 and x86_64. The app bundle contains a universal Go helper at `Contents/Helpers/mcpaste`; the helper is not published as a separate artifact.

## Release flow

The tag workflow checks out the tagged commit with pinned actions, creates the GitHub Release when absent, or verifies the existing Release when present. It then invokes the reusable macOS workflow with the same tag.

The macOS workflow:

1. verifies that the input is an existing `v*` tag at the checked-out commit and that its GitHub Release exists;
2. generates the full dependency license texts for `./cmd/mcpaste` with pinned `go-licenses` after Go setup;
3. runs the Swift test suite;
4. builds the Swift executable separately for arm64 and x86_64, verifies the resource bundle is architecture-neutral, and combines the executables into one universal app;
5. cross-builds `./cmd/mcpaste` for Darwin arm64 and amd64 with CGO disabled, then combines both helper executables;
6. verifies that the app and embedded helper each contain exactly arm64 and x86_64 before signing;
7. signs the helper before signing the app;
8. notarizes and staples a Developer ID build when the protected Apple credentials are configured;
9. otherwise applies an ad-hoc signature;
10. uploads the app archive, `macOS-SHA256SUMS`, `THIRD_PARTY_NOTICES.md`, and `THIRD_PARTY_LICENSES.tar.gz` to the verified tag.

No standalone helper is uploaded.

## Artifacts

- `MCPaste-final.zip`: Developer ID signed, notarized, and stapled universal app with its universal embedded helper
- `MCPaste-adhoc.zip`: ad-hoc-signed universal fallback with its universal embedded helper when Apple credentials are absent
- `macOS-SHA256SUMS`: checksum for the app archive produced by that run
- `THIRD_PARTY_NOTICES.md`: notice index for dependencies embedded in the app
- `THIRD_PARTY_LICENSES.tar.gz`: generated full license texts for the embedded helper's dependency graph

The installer prefers `MCPaste-final.zip` and falls back to `MCPaste-adhoc.zip`. It verifies the selected archive against `macOS-SHA256SUMS` before replacing `/Applications/MCPaste.app`.

## Verify a download

Run these commands from the directory containing the downloaded files:

```sh
shasum -a 256 --check macOS-SHA256SUMS
ditto -xk MCPaste-final.zip /tmp/mcpaste-release-check
codesign --verify --deep --strict --verbose=2 /tmp/mcpaste-release-check/MCPaste.app
spctl --assess --type execute --verbose=4 /tmp/mcpaste-release-check/MCPaste.app
test -x /tmp/mcpaste-release-check/MCPaste.app/Contents/Helpers/mcpaste
lipo -archs /tmp/mcpaste-release-check/MCPaste.app/Contents/MacOS/MCPaste
lipo -archs /tmp/mcpaste-release-check/MCPaste.app/Contents/Helpers/mcpaste
```

Both `lipo -archs` commands must report exactly `arm64` and `x86_64`; their printed order may differ.

For the ad-hoc fallback, use `MCPaste-adhoc.zip`; checksum and `codesign --verify` still apply, while notarization assessment is not expected to pass.

## Release gate

Before creating a tag, run the full CI command set, inspect `git diff`, run the hosted-term and secret-pattern audits, and verify the app on macOS 14 or later. Tag creation, signing, notarization, and upload are explicit maintainer actions.
