import AppKit
import Combine
import MCPasteCore
import SwiftUI
import XCTest
@testable import MCPasteApp

private enum SnapshotRenderFailure: Error {
    case bitmapRepresentation
    case pngEncoding
    case missingOrEmptyArtifact(String)
}

private enum SnapshotArtifactValidator {
    static func validate(directory: URL, expectedNames: [String]) throws {
        for name in expectedNames {
            let url = directory.appendingPathComponent("\(name).png")
            guard let data = try? Data(contentsOf: url), !data.isEmpty else {
                throw SnapshotRenderFailure.missingOrEmptyArtifact(name)
            }
        }
    }
}

private actor SnapshotRuntime: PeerRuntimeServing {
    let context: RuntimeContext
    let listedDevices: [PeerDevice]

    init(context: RuntimeContext, devices: [PeerDevice]) {
        self.context = context
        listedDevices = devices
    }

    func publish(
        text: String,
        images: [NormalizedImage],
        expectedRevision: RuntimeRevision?
    ) async throws -> PublicationResult {
        PublicationResult(
            revision: RuntimeRevision(wallMillis: 1, logical: 0, deviceID: "snapshot"),
            syncState: .upToDate
        )
    }
    func current() async throws -> RuntimeContext? { context }
    func devices() async throws -> [PeerDevice] { listedDevices }
}

@MainActor
final class SnapshotRenderTests: XCTestCase {
    private static let expectedSnapshotNames = [
        "task9-popovers-combined",
        "task9-popover-many-devices",
        "task9-content-empty-minimum",
        "task9-content-populated-minimum",
        "task9-content-source-offline"
    ]

    private var outputDirectory: URL? {
        guard let path = ProcessInfo.processInfo.environment["MCPASTE_SNAPSHOT_DIR"], !path.isEmpty else {
            return nil
        }
        return URL(fileURLWithPath: path, isDirectory: true)
    }

    func testOfflineDeviceFixtureKeepsLocalReachableAndMakesTheSourceUnavailable() throws {
        let devices = fixtureDevices(
            now: Date(timeIntervalSince1970: 1_000_000),
            sourceReachable: false
        )
        let local = try XCTUnwrap(devices.first(where: \.isLocal))
        let source = try XCTUnwrap(devices.first(where: \.isSource))

        XCTAssertTrue(local.reachable)
        XCTAssertTrue(source.isSource)
        XCTAssertFalse(source.reachable)
    }

    func testManyDevicePopoverKeepsFixedChromeWithin430Points() async {
        let now = Date(timeIntervalSince1970: 1_000_000)
        let model = await fixtureModel(
            now: now,
            text: "many devices",
            images: [],
            devices: manyDevices(now: now),
            sourceReachable: true,
            state: .upToDate,
            connectors: ["Codex", "Claude Code"]
        )
        let hosting = NSHostingView(rootView: StatusPopoverView(model: model).frame(width: 300))
        hosting.layoutSubtreeIfNeeded()

        XCTAssertLessThanOrEqual(hosting.fittingSize.height, 430)
    }

    func testFixtureWaitsForExactConnectorNames() async {
        let requested = ["Codex", "Claude Code"]
        let model = await fixtureModel(
            now: Date(timeIntervalSince1970: 1_000_000),
            text: "connector fixture",
            images: [],
            devices: [],
            sourceReachable: true,
            state: .upToDate,
            connectors: requested
        )

        XCTAssertEqual(model.connectorNames, requested)
    }

    func testSnapshotArtifactValidationRejectsMissingAndEmptyPNGs() throws {
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("mcpaste-snapshot-validation-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        FileManager.default.createFile(
            atPath: directory.appendingPathComponent("empty.png").path,
            contents: Data()
        )

        XCTAssertThrowsError(try SnapshotArtifactValidator.validate(
            directory: directory,
            expectedNames: ["empty", "missing"]
        ))
    }

    func testRenderScreens() async throws {
        guard ProcessInfo.processInfo.environment["MCPASTE_RENDER_SNAPSHOTS"] == "1" else {
            throw XCTSkip("snapshot rendering disabled")
        }
        let outputDirectory = try XCTUnwrap(
            outputDirectory,
            "MCPASTE_SNAPSHOT_DIR is required when snapshot rendering is enabled"
        )

        let now = Date()
        let devices = fixtureDevices(now: now, sourceReachable: true)
        let offlineDevices = fixtureDevices(now: now, sourceReachable: false)
        let images = [swatch(.systemBlue, .systemPurple), swatch(.systemOrange, .systemPink)]
        let populated = await fixtureModel(
            now: now,
            text: "MCPaste local test line 1\nline 2 with trailing spaces   ",
            images: images,
            devices: devices,
            sourceReachable: true,
            state: .upToDate,
            connectors: ["Codex", "Claude Code"]
        )
        let offline = await fixtureModel(
            now: now,
            text: "This source-offline replica remains visible.\nExact spacing stays here.   ",
            images: images,
            devices: offlineDevices,
            sourceReachable: false,
            state: .sourceOffline,
            connectors: ["Codex", "Claude Code"]
        )
        let empty = await fixtureModel(
            now: now,
            text: "",
            images: [],
            devices: devices,
            sourceReachable: true,
            state: .waitingToSync,
            connectors: []
        )
        let manyDeviceModel = await fixtureModel(
            now: now,
            text: "Many nearby peer devices remain bounded.",
            images: [],
            devices: manyDevices(now: now),
            sourceReachable: true,
            state: .upToDate,
            connectors: ["Codex", "Claude Code"]
        )

        try render(
            HStack(spacing: 0) {
                StatusPopoverView(model: populated)
                    .frame(width: 300, height: 430)
                StatusPopoverView(model: offline)
                    .frame(width: 300, height: 430)
            },
            size: CGSize(width: 600, height: 430),
            name: "task9-popovers-combined",
            outputDirectory: outputDirectory
        )
        try render(
            StatusPopoverView(model: manyDeviceModel)
                .frame(width: 300, height: 430),
            size: CGSize(width: 300, height: 430),
            name: "task9-popover-many-devices",
            outputDirectory: outputDirectory
        )
        try render(
            ContentWindowView(model: empty),
            size: CGSize(width: 720, height: 460),
            name: "task9-content-empty-minimum",
            outputDirectory: outputDirectory
        )
        try render(
            ContentWindowView(model: populated),
            size: CGSize(width: 720, height: 460),
            name: "task9-content-populated-minimum",
            outputDirectory: outputDirectory
        )
        try render(
            ContentWindowView(model: offline),
            size: CGSize(width: 720, height: 460),
            name: "task9-content-source-offline",
            outputDirectory: outputDirectory
        )
        try SnapshotArtifactValidator.validate(
            directory: outputDirectory,
            expectedNames: Self.expectedSnapshotNames
        )
    }

    private func fixtureModel(
        now: Date,
        text: String,
        images: [NormalizedImage],
        devices: [PeerDevice],
        sourceReachable: Bool,
        state: PeerSyncState,
        connectors: [String]
    ) async -> AppModel {
        let configurationCompleted = expectation(description: "connector configuration completed")
        let context = RuntimeContext(
            revision: RuntimeRevision(wallMillis: 10, logical: 0, deviceID: "studio"),
            sourceDeviceID: "studio",
            updatedAt: now,
            text: text,
            assets: images.enumerated().map { index, image in
                RuntimeAsset(
                    digest: String(repeating: String(index + 1), count: 64),
                    mimeType: image.mimeType,
                    width: image.width,
                    height: image.height,
                    data: image.data
                )
            },
            sourceReachable: sourceReachable,
            syncState: state
        )
        let model = AppModel(
            runtime: SnapshotRuntime(context: context, devices: devices),
            automaticallyRefresh: false,
            normalize: { _ in [] },
            configureConnectors: {
                configurationCompleted.fulfill()
                return connectors
            }
        )
        let namesApplied = expectation(description: "connector names applied")
        let subscription = model.$connectorNames
            .filter { $0 == connectors }
            .first()
            .sink { _ in namesApplied.fulfill() }
        await model.refreshNow()
        await fulfillment(of: [configurationCompleted, namesApplied], timeout: 1)
        withExtendedLifetime(subscription) {}
        XCTAssertEqual(model.connectorNames, connectors)
        return model
    }

    private func fixtureDevices(now: Date, sourceReachable: Bool) -> [PeerDevice] {
        [
            PeerDevice(
                id: "local",
                displayName: "Blanc’s MacBook Pro with a long local device name",
                reachable: true,
                isLocal: true,
                isSource: false,
                lastSeenAt: now
            ),
            PeerDevice(
                id: "studio",
                displayName: "Studio Mac — Current Context Source with a Very Long Name",
                reachable: sourceReachable,
                isLocal: false,
                isSource: true,
                lastSeenAt: now
            ),
            PeerDevice(
                id: "travel",
                displayName: "Travel Mac currently unavailable",
                reachable: false,
                isLocal: false,
                isSource: false,
                lastSeenAt: now.addingTimeInterval(-3_600)
            )
        ]
    }

    private func manyDevices(now: Date) -> [PeerDevice] {
        fixtureDevices(now: now, sourceReachable: true) + (0..<18).map { index in
            PeerDevice(
                id: "extra-\(index)",
                displayName: "Additional nearby Mac \(index + 1) with a long device name",
                reachable: index.isMultiple(of: 3),
                isLocal: false,
                isSource: false,
                lastSeenAt: now.addingTimeInterval(TimeInterval(-index * 60))
            )
        }
    }

    private func swatch(_ start: NSColor, _ end: NSColor) -> NormalizedImage {
        let size = NSSize(width: 88, height: 64)
        let image = NSImage(size: size)
        image.lockFocus()
        NSGradient(starting: start, ending: end)?.draw(in: NSRect(origin: .zero, size: size), angle: 45)
        image.unlockFocus()
        let data = image.tiffRepresentation.flatMap {
            NSBitmapImageRep(data: $0)?.representation(using: .png, properties: [:])
        } ?? Data()
        return NormalizedImage(mimeType: "image/png", width: 88, height: 64, data: data)
    }

    private func render(
        _ view: some View,
        size: CGSize,
        name: String,
        outputDirectory: URL
    ) throws {
        let hosting = NSHostingView(
            rootView: view
                .frame(width: size.width, height: size.height)
                .background(Color(nsColor: .windowBackgroundColor))
        )
        hosting.frame = CGRect(origin: .zero, size: size)
        hosting.appearance = NSAppearance(named: .aqua)

        let window = NSWindow(
            contentRect: hosting.frame,
            styleMask: [.titled, .closable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.contentView = hosting
        window.appearance = NSAppearance(named: .aqua)
        window.layoutIfNeeded()
        hosting.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.45))

        guard let rep = hosting.bitmapImageRepForCachingDisplay(in: hosting.bounds) else {
            throw SnapshotRenderFailure.bitmapRepresentation
        }
        hosting.cacheDisplay(in: hosting.bounds, to: rep)
        guard let data = rep.representation(using: .png, properties: [:]) else {
            throw SnapshotRenderFailure.pngEncoding
        }
        let url = outputDirectory.appendingPathComponent("\(name).png")
        try data.write(to: url)
        print("SNAPSHOT \(url.path)")
    }
}
