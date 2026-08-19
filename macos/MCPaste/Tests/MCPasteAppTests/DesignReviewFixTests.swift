import Foundation
import MCPasteCore
import SwiftUI
import XCTest
@testable import MCPasteApp

@MainActor
final class DesignReviewFixTests: XCTestCase {
    func testOpeningContentWindowActivatesAndDismissesInOrder() {
        var calls: [String] = []

        ContentWindowOpener.open(
            openWindow: { calls.append("open") },
            activate: { calls.append("activate") },
            dismissPopover: { calls.append("dismiss") }
        )

        XCTAssertEqual(calls, ["open", "activate", "dismiss"])
    }

    func testAutoOpenActivatesWithoutAPopoverToDismiss() {
        var calls: [String] = []

        ContentWindowOpener.open(
            openWindow: { calls.append("open") },
            activate: { calls.append("activate") }
        )

        XCTAssertEqual(calls, ["open", "activate"])
    }

    func testEditorStatusUsesTheApprovedExactCopy() {
        XCTAssertEqual(EditorStatus.text(for: .upToDate), "Up to date")
        XCTAssertEqual(EditorStatus.text(for: .updating), "Updating…")
        XCTAssertEqual(EditorStatus.text(for: .waitingToSync), "Waiting to sync")
        XCTAssertEqual(EditorStatus.text(for: .sourceOffline), "Source offline")
    }

    func testConnectorConfigurationCopyUsesOnlyTheConfiguredCount() {
        XCTAssertEqual(ConnectorConfigurationCopy.text(count: 0), "0 MCP connectors configured")
        XCTAssertEqual(ConnectorConfigurationCopy.text(count: 1), "1 MCP connector configured")
        XCTAssertEqual(ConnectorConfigurationCopy.text(count: 2), "2 MCP connectors configured")
    }

    func testBlankContextActionUsesActionOrientedAccessibilityCopy() {
        XCTAssertEqual(BlankContextAction.accessibilityLabel, "Start a blank shared context")
    }

    func testCurrentSwiftUISourcesExcludeRetiredVocabulary() throws {
        let sourceRoot = try appSourceRoot()
        let forbidden = [
            "This Mac",
            "Approve a device",
            "Create workspace",
            "Join workspace",
            "Recovery code",
            "Search history",
            "Paste #"
        ]
        guard let enumerator = FileManager.default.enumerator(
            at: sourceRoot,
            includingPropertiesForKeys: nil
        ) else {
            return XCTFail("Could not enumerate current app sources at \(sourceRoot.path)")
        }

        var auditedFiles = 0
        for case let url as URL in enumerator where url.pathExtension == "swift" {
            let source = try String(contentsOf: url, encoding: .utf8)
            guard source.contains("import SwiftUI") else { continue }
            auditedFiles += 1
            for phrase in forbidden {
                XCTAssertFalse(
                    source.contains(phrase),
                    "Found retired UI copy '\(phrase)' in \(url.lastPathComponent)"
                )
            }
        }
        XCTAssertGreaterThan(auditedFiles, 0, "The source audit must inspect current SwiftUI files")
    }

    func testCurrentContextSubtitleMapsSourceAndTime() {
        let updated = Date(timeIntervalSince1970: 1_000_000)

        XCTAssertEqual(
            EditorStatus.subtitle(lastUpdatedBy: nil, lastUpdatedAt: nil, now: updated),
            "Changes sync automatically"
        )
        XCTAssertEqual(
            EditorStatus.subtitle(lastUpdatedBy: "Studio Mac", lastUpdatedAt: updated, now: updated.addingTimeInterval(2)),
            "Updated from Studio Mac · just now"
        )
    }

    func testDevicePresentationDistinguishesLocalSourceAndReachabilityWithoutColorAlone() {
        let now = Date(timeIntervalSince1970: 1_000_000)
        let local = PeerDevice(
            id: "local",
            displayName: "Local Mac",
            reachable: true,
            isLocal: true,
            isSource: false,
            lastSeenAt: now
        )
        let source = PeerDevice(
            id: "source",
            displayName: "Source Mac",
            reachable: true,
            isLocal: false,
            isSource: true,
            lastSeenAt: now
        )
        let unavailable = PeerDevice(
            id: "away",
            displayName: "Away Mac",
            reachable: false,
            isLocal: false,
            isSource: false,
            lastSeenAt: now
        )

        XCTAssertTrue(DeviceRowPresentation(local).highlightsLocalDevice)
        XCTAssertTrue(DeviceRowPresentation(local).accessibilityLabel.contains("Local device"))
        XCTAssertTrue(DeviceRowPresentation(source).showsSourceBadge)
        XCTAssertTrue(DeviceRowPresentation(source).accessibilityLabel.contains("Current source"))
        XCTAssertFalse(DeviceRowPresentation(unavailable).reachable)
        XCTAssertTrue(DeviceRowPresentation(unavailable).accessibilityLabel.contains("Unavailable"))
    }

    func testRelativeTimeStaysCurrent() {
        let updated = Date(timeIntervalSince1970: 1_000_000)

        XCTAssertEqual(RelativeTime.string(for: updated, now: updated.addingTimeInterval(2)), "just now")
        XCTAssertEqual(RelativeTime.string(for: updated, now: updated.addingTimeInterval(20)), "20 seconds ago")
        XCTAssertEqual(RelativeTime.string(for: updated, now: updated.addingTimeInterval(660)), "11 minutes ago")
    }

    private func appSourceRoot() throws -> URL {
        var directory = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        while directory.pathComponents.count > 1 {
            let candidate = directory.appendingPathComponent("Sources/MCPasteApp", isDirectory: true)
            var isDirectory: ObjCBool = false
            if FileManager.default.fileExists(atPath: candidate.path, isDirectory: &isDirectory),
               isDirectory.boolValue
            {
                return candidate
            }
            directory.deleteLastPathComponent()
        }
        throw NSError(domain: "DesignReviewFixTests", code: 1)
    }
}
