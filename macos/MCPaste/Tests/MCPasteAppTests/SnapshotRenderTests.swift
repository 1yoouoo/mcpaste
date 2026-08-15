import XCTest
import SwiftUI
import AppKit
import MCPasteCore
@testable import MCPasteApp

/// Temporary design-review harness: renders real app views to PNG so layouts can be inspected.
@MainActor
final class SnapshotRenderTests: XCTestCase {
    private var outputDirectory: URL {
        URL(fileURLWithPath: ProcessInfo.processInfo.environment["MCPASTE_SNAPSHOT_DIR"] ?? NSTemporaryDirectory())
    }

    func testRenderScreens() throws {
        try XCTSkipIf(ProcessInfo.processInfo.environment["MCPASTE_SNAPSHOT_DIR"] == nil, "snapshot rendering disabled")

        let history = [
            CachedPaste(id: "p1", revisionID: "r1", sequence: 42, text: "MCPaste local test line 1\nline 2 with trailing spaces   ", deleted: false, expiresAt: Date().addingTimeInterval(7200), attachmentRevisionID: "rev", attachments: [attachment(0), attachment(1)]),
            CachedPaste(id: "p2", revisionID: "r2", sequence: 41, text: "npm run build -- --workspace=@acme/web", deleted: false, expiresAt: Date().addingTimeInterval(7200)),
            CachedPaste(id: "p3", revisionID: "r3", sequence: 40, text: nil, deleted: false, expiresAt: Date().addingTimeInterval(7200), attachmentRevisionID: "rev", attachments: [attachment(0), attachment(1), attachment(2)]),
            CachedPaste(id: "p4", revisionID: "r4", sequence: 39, text: "SELECT * FROM pastes WHERE workspace_id = $1 ORDER BY sequence DESC LIMIT 50", deleted: false, expiresAt: Date().addingTimeInterval(7200)),
            CachedPaste(id: "p5", revisionID: "r5", sequence: 38, text: nil, deleted: true, expiresAt: Date().addingTimeInterval(7200))
        ]
        let devices = [
            DeviceRecord(id: "d1", displayName: "Blanc MacBook Pro", platform: "macOS", role: "full", isCurrent: true),
            DeviceRecord(id: "d2", displayName: "linux-companion", platform: "linux", role: "read-only", isCurrent: false)
        ]
        let images = [swatch(.systemBlue, .systemPurple), swatch(.systemOrange, .systemPink)]
        let synced = Date().addingTimeInterval(-12)

        let popoverSize = CGSize(width: 300, height: 360)
        let contentSize = CGSize(width: 760, height: 520)

        try render(StatusPopoverView(model: seed {
            $0.previewSeed(history: history, devices: devices, workspaceID: "ws_7f3a91c4e2", lastSyncedAt: synced)
        }), size: popoverSize, name: "n01-popover-realtime")

        try render(StatusPopoverView(model: seed {
            $0.previewSeed(history: history, devices: devices, pending: true, pendingCount: 3, syncStatus: .polling, lastSyncedAt: synced)
        }), size: popoverSize, name: "n02-popover-pending")

        try render(StatusPopoverView(model: seed {
            $0.previewSeed(history: history, devices: devices, pendingCount: 1, syncStatus: .polling, errorMessage: "Sync unavailable.", lastSyncedAt: Date().addingTimeInterval(-480))
        }), size: popoverSize, name: "n03-popover-offline")

        try render(StatusPopoverView(model: seed { $0.previewSeed(screen: .onboarding) }), size: CGSize(width: 320, height: 340), name: "n04-onboarding")

        try render(StatusPopoverView(model: seed { model in
            model.previewSeed(screen: .recovery)
            model.recoveryCode = "MOCK-CODE-NOT-REAL-0000"
        }), size: CGSize(width: 340, height: 340), name: "n05-recovery")

        try render(ContentWindowView(model: seed { $0.previewSeed() }), size: contentSize, name: "n06-content-empty")

        try render(ContentWindowView(model: seed { model in
            model.previewSeed(
                draft: "MCPaste local test line 1\nline 2 with trailing spaces   ",
                history: history,
                devices: devices,
                selectedPasteID: "p1",
                attachments: images,
                lastSyncedAt: synced
            )
        }), size: contentSize, name: "n07-content-populated")

        try render(ContentWindowView(model: seed { model in
            model.previewSeed(
                draft: "MCPaste local test line 1\nline 2 with trailing spaces   ",
                history: history,
                devices: devices,
                selectedPasteID: "p1",
                attachments: images,
                lastSyncedAt: synced
            )
        }), size: CGSize(width: 1200, height: 800), name: "n08-content-large")
    }

    private func attachment(_ index: Int) -> PasteAttachment {
        PasteAttachment(assetIndex: index, mimeType: "image/png", width: 320, height: 240, byteSize: 1024, expiresAt: Date().addingTimeInterval(7200))
    }

    private func swatch(_ start: NSColor, _ end: NSColor) -> NormalizedImage {
        let size = NSSize(width: 88, height: 64)
        let image = NSImage(size: size)
        image.lockFocus()
        NSGradient(starting: start, ending: end)?.draw(in: NSRect(origin: .zero, size: size), angle: 45)
        image.unlockFocus()
        let data = image.tiffRepresentation.flatMap { NSBitmapImageRep(data: $0)?.representation(using: .png, properties: [:]) } ?? Data()
        return NormalizedImage(mimeType: "image/png", width: 88, height: 64, data: data)
    }

    private func seed(_ configure: (AppModel) -> Void) -> AppModel {
        let model = AppModel(keychain: KeychainStore(service: "com.mcpaste.snapshot.\(UUID().uuidString)"))
        configure(model)
        return model
    }

    private func render(_ view: some View, size: CGSize, name: String) throws {
        let hosting = NSHostingView(rootView: view.frame(width: size.width, height: size.height))
        hosting.frame = CGRect(origin: .zero, size: size)
        hosting.appearance = NSAppearance(named: .aqua)

        let window = NSWindow(contentRect: hosting.frame, styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
        window.contentView = hosting
        window.appearance = NSAppearance(named: .aqua)
        window.layoutIfNeeded()
        hosting.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.45))

        guard let rep = hosting.bitmapImageRepForCachingDisplay(in: hosting.bounds) else {
            throw XCTSkip("no bitmap representation")
        }
        hosting.cacheDisplay(in: hosting.bounds, to: rep)
        guard let data = rep.representation(using: .png, properties: [:]) else {
            throw XCTSkip("no png representation")
        }
        try data.write(to: outputDirectory.appendingPathComponent("\(name).png"))
    }
}
