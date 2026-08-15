import XCTest
@testable import MCPasteCore

final class PendingAttachmentStoreTests: XCTestCase {
    private func temporaryDirectory() throws -> URL {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("mcpaste-pending-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    func testManifestSurvivesRestartAndCleanupRemovesOnlyOrphans() throws {
        let root = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: root) }
        let image = NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([1]))
        let store = try PendingAttachmentStore(root: root)
        let manifest = try store.persist([image])
        let orphan = try store.persist([NormalizedImage(mimeType: "image/jpeg", width: 2, height: 3, data: Data([2, 3]))])

        let restarted = try PendingAttachmentStore(root: root)
        XCTAssertEqual(try restarted.load(manifest).map(\.data), [image.data])
        try restarted.removeOrphans(referencedPaths: Set(manifest.items.map(\.path)))
        XCTAssertTrue(FileManager.default.fileExists(atPath: manifest.items[0].path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: orphan.items[0].path))

        try restarted.remove(manifest)
        XCTAssertFalse(FileManager.default.fileExists(atPath: manifest.items[0].path))
    }

    func testManifestRejectsPathOutsideInjectedQueueRoot() throws {
        let root = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = try PendingAttachmentStore(root: root)
        let manifest = try store.persist([NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([1]))])
        let outside = root.deletingLastPathComponent().appendingPathComponent("outside-\(UUID().uuidString)")
        try Data([9]).write(to: outside)
        defer { try? FileManager.default.removeItem(at: outside) }

        let forged = PendingAttachmentManifest(items: [
            .init(path: outside.path, mimeType: manifest.items[0].mimeType, width: 1, height: 1)
        ])
        XCTAssertThrowsError(try store.load(forged))
        XCTAssertThrowsError(try store.remove(forged))
        XCTAssertEqual(try Data(contentsOf: outside), Data([9]))
    }
}
