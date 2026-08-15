import Foundation
import XCTest
@testable import MCPasteCore

final class AttachmentCacheTests: XCTestCase {
    private let pasteID = "11111111-1111-4111-8111-111111111111"
    private let otherPasteID = "22222222-2222-4222-8222-222222222222"
    private let revisionID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
    private let otherRevisionID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

    func testCacheKeyIncludesPasteRevisionAndAssetIndex() throws {
        let root = try temporaryDirectory()
        defer { removeTemporaryDirectory(root) }
        let cache = try AttachmentCache(root: root)
        let key = try AttachmentCache.Key(pasteID: pasteID, revisionID: revisionID, assetIndex: 0)

        try cache.store(Data([1, 2, 3]), for: key)

        XCTAssertEqual(try cache.data(for: key), Data([1, 2, 3]))
        let missing = try AttachmentCache.Key(pasteID: pasteID, revisionID: revisionID, assetIndex: 1)
        XCTAssertNil(try cache.data(for: missing))
        try cache.removeOtherRevisions(forPasteID: pasteID, keeping: revisionID)
        XCTAssertEqual(try cache.data(for: key), Data([1, 2, 3]))
    }

    func testStoreReplacesExistingBytesAndRestartCanReuseCache() throws {
        let root = try temporaryDirectory()
        defer { removeTemporaryDirectory(root) }
        let key = try AttachmentCache.Key(pasteID: pasteID, revisionID: revisionID, assetIndex: 0)

        try AttachmentCache(root: root).store(Data([1]), for: key)
        let restartedCache = try AttachmentCache(root: root)
        try restartedCache.store(Data([2, 3]), for: key)

        XCTAssertEqual(try restartedCache.data(for: key), Data([2, 3]))
    }

    func testRemoveOtherRevisionsKeepsSelectedBundleAndDeletesOldBundle() throws {
        let root = try temporaryDirectory()
        defer { removeTemporaryDirectory(root) }
        let cache = try AttachmentCache(root: root)
        let currentKey = try AttachmentCache.Key(pasteID: pasteID, revisionID: revisionID, assetIndex: 0)
        let oldKey = try AttachmentCache.Key(pasteID: pasteID, revisionID: otherRevisionID, assetIndex: 0)
        try cache.store(Data([1]), for: currentKey)
        try cache.store(Data([2]), for: oldKey)

        try cache.removeOtherRevisions(forPasteID: pasteID, keeping: revisionID)

        XCTAssertEqual(try cache.data(for: currentKey), Data([1]))
        XCTAssertNil(try cache.data(for: oldKey))
    }

    func testRemoveAllDeletesOnlyTheRequestedPaste() throws {
        let root = try temporaryDirectory()
        defer { removeTemporaryDirectory(root) }
        let cache = try AttachmentCache(root: root)
        let firstKey = try AttachmentCache.Key(pasteID: pasteID, revisionID: revisionID, assetIndex: 0)
        let secondKey = try AttachmentCache.Key(pasteID: otherPasteID, revisionID: revisionID, assetIndex: 0)
        try cache.store(Data([1]), for: firstKey)
        try cache.store(Data([2]), for: secondKey)

        try cache.removeAll(forPasteID: pasteID)

        XCTAssertNil(try cache.data(for: firstKey))
        XCTAssertEqual(try cache.data(for: secondKey), Data([2]))
    }

    func testInvalidUUIDTraversalAndAssetIndexAreRejectedBeforeCaching() throws {
        let root = try temporaryDirectory()
        defer { removeTemporaryDirectory(root) }

        for invalidPasteID in ["not-a-uuid", "../outside", "11111111111141118111111111111111"] {
            XCTAssertThrowsError(try AttachmentCache.Key(pasteID: invalidPasteID, revisionID: revisionID, assetIndex: 0))
        }
        for invalidRevisionID in ["not-a-uuid", "../../outside", "{aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}"] {
            XCTAssertThrowsError(try AttachmentCache.Key(pasteID: pasteID, revisionID: invalidRevisionID, assetIndex: 0))
        }
        for invalidAssetIndex in [-1, 8, Int.max] {
            XCTAssertThrowsError(try AttachmentCache.Key(pasteID: pasteID, revisionID: revisionID, assetIndex: invalidAssetIndex))
        }

        let cache = try AttachmentCache(root: root)
        let outside = root.deletingLastPathComponent().appendingPathComponent("mcpaste-cache-outside-marker")
        try Data([9]).write(to: outside)
        defer { try? FileManager.default.removeItem(at: outside) }

        XCTAssertThrowsError(try cache.removeAll(forPasteID: "../\(outside.lastPathComponent)"))
        XCTAssertEqual(try Data(contentsOf: outside), Data([9]))
    }

    func testSymlinkedRootAndCacheDirectoriesCannotEscapeRoot() throws {
        let root = try temporaryDirectory()
        defer { removeTemporaryDirectory(root) }
        let outside = try temporaryDirectory()
        defer { removeTemporaryDirectory(outside) }
        let key = try AttachmentCache.Key(pasteID: pasteID, revisionID: revisionID, assetIndex: 0)
        let outsideFile = outside.appendingPathComponent("outside")
        try Data([9]).write(to: outsideFile)

        let linkedRoot = root.appendingPathComponent("linked-root")
        try FileManager.default.createSymbolicLink(at: linkedRoot, withDestinationURL: outside)
        XCTAssertThrowsError(try AttachmentCache(root: linkedRoot))

        let cacheRoot = root.appendingPathComponent("cache-root")
        try FileManager.default.createDirectory(at: cacheRoot, withIntermediateDirectories: true)
        let cache = try AttachmentCache(root: cacheRoot)
        let linkedPaste = cacheRoot.appendingPathComponent(pasteID)
        try FileManager.default.createSymbolicLink(at: linkedPaste, withDestinationURL: outside)
        XCTAssertThrowsError(try cache.store(Data([1]), for: key))
        XCTAssertThrowsError(try cache.data(for: key))
        XCTAssertThrowsError(try cache.removeOtherRevisions(forPasteID: pasteID, keeping: revisionID))
        XCTAssertThrowsError(try cache.removeAll(forPasteID: pasteID))
        XCTAssertEqual(try Data(contentsOf: outsideFile), Data([9]))

        if FileManager.default.fileExists(atPath: linkedPaste.path) {
            try FileManager.default.removeItem(at: linkedPaste)
        }
        try FileManager.default.createDirectory(at: linkedPaste, withIntermediateDirectories: true)
        let linkedRevision = linkedPaste.appendingPathComponent(revisionID)
        try FileManager.default.createSymbolicLink(at: linkedRevision, withDestinationURL: outside)
        XCTAssertThrowsError(try cache.store(Data([2]), for: key))
        XCTAssertThrowsError(try cache.data(for: key))
        XCTAssertEqual(try Data(contentsOf: outsideFile), Data([9]))

        try FileManager.default.removeItem(at: linkedRevision)
        try FileManager.default.createDirectory(at: linkedRevision, withIntermediateDirectories: true)
        let linkedFile = linkedRevision.appendingPathComponent("0")
        try FileManager.default.createSymbolicLink(at: linkedFile, withDestinationURL: outsideFile)
        XCTAssertThrowsError(try cache.store(Data([3]), for: key))
        XCTAssertThrowsError(try cache.data(for: key))
        XCTAssertEqual(try Data(contentsOf: outsideFile), Data([9]))
    }

    private func temporaryDirectory() throws -> URL {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("mcpaste-attachment-cache-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        return root
    }

    private func removeTemporaryDirectory(_ root: URL) {
        try? FileManager.default.removeItem(at: root)
    }
}
