# MCPaste macOS text phase handoff

Date: 2026-08-13

## Implemented

- Added a macOS 14 Swift Package with `MCPasteCore` and `MCPasteApp` targets.
- Added authenticated JSON API methods for workspace creation, pairing claim, paste mutations, sync, and device operations.
- Added Keychain Services storage keyed by workspace, device, and scope; connector credentials are not reused as full credentials.
- Added SQLite cache tables for exact text revisions, durable sync cursor, and ordered offline mutations with stable idempotency keys.
- Added a SwiftUI `MenuBarExtra` onboarding and text editor flow with pending-state and deletion controls.

## Verification

- `swiftc -typecheck` passed for all core sources using the installed macOS 14 SDK.
- `swiftc -typecheck` passed for the app target against the checked core module.
- `swift test` was attempted and could not start because the active Command Line Tools installation does not expose the SDK `PlatformPath` metadata required by SwiftPM.
- `xcodebuild` was attempted and could not start because the active developer directory is Command Line Tools, not a full Xcode installation.
- Go verification remains part of the final Phase 6 acceptance pass.

## Owner action before shipping

- Install/select a full Xcode version compatible with macOS 14, then run `swift test`, `swift build -c release`, and the macOS archive/signing checks from the delivery runbook.
- Review the app’s endpoint onboarding policy and provide the production HTTPS endpoint only through the user-facing configuration path.
