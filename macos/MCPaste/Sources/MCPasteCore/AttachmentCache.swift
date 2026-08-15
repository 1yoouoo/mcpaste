import Foundation

private enum AttachmentCacheError: Error {
    case invalidIdentifier
    case invalidAssetIndex
    case invalidRoot
    case invalidEntry
}

public final class AttachmentCache {
    public struct Key: Hashable {
        public let pasteID: String
        public let revisionID: String
        public let assetIndex: Int

        public init(pasteID: String, revisionID: String, assetIndex: Int) throws {
            guard AttachmentCache.isCanonicalUUID(pasteID), AttachmentCache.isCanonicalUUID(revisionID) else {
                throw AttachmentCacheError.invalidIdentifier
            }
            guard (0...7).contains(assetIndex) else {
                throw AttachmentCacheError.invalidAssetIndex
            }
            self.pasteID = pasteID
            self.revisionID = revisionID
            self.assetIndex = assetIndex
        }
    }

    private let fileManager: FileManager
    private let root: URL

    public init(root: URL) throws {
        let fileManager = FileManager.default
        let standardizedRoot = root.standardizedFileURL
        if let type = Self.fileType(at: standardizedRoot, fileManager: fileManager) {
            guard type == .typeDirectory else { throw AttachmentCacheError.invalidRoot }
        } else {
            try fileManager.createDirectory(at: standardizedRoot, withIntermediateDirectories: true)
            guard Self.fileType(at: standardizedRoot, fileManager: fileManager) == .typeDirectory else {
                throw AttachmentCacheError.invalidRoot
            }
        }
        self.fileManager = fileManager
        self.root = standardizedRoot
    }

    public func data(for key: Key) throws -> Data? {
        guard try ensureExistingDirectory(directoryURL(forPasteID: key.pasteID)) else { return nil }
        guard try ensureExistingDirectory(
            directoryURL(forPasteID: key.pasteID).appendingPathComponent(key.revisionID, isDirectory: true)
        ) else { return nil }
        let file = fileURL(for: key)
        guard let type = Self.fileType(at: file, fileManager: fileManager) else { return nil }
        guard type == .typeRegular else {
            throw AttachmentCacheError.invalidEntry
        }
        return try Data(contentsOf: file)
    }

    public func store(_ data: Data, for key: Key) throws {
        try ensureDirectory(root)
        let pasteDirectory = directoryURL(forPasteID: key.pasteID)
        let revisionDirectory = pasteDirectory.appendingPathComponent(key.revisionID, isDirectory: true)
        try ensureDirectory(pasteDirectory)
        try ensureDirectory(revisionDirectory)
        let file = fileURL(for: key)
        if let type = Self.fileType(at: file, fileManager: fileManager) {
            guard type == .typeRegular else { throw AttachmentCacheError.invalidEntry }
        }
        try data.write(to: file, options: .atomic)
    }

    public func remove(_ key: Key) throws {
        let file = fileURL(for: key)
        guard let type = Self.fileType(at: file, fileManager: fileManager) else { return }
        guard type == .typeRegular else { throw AttachmentCacheError.invalidEntry }
        try fileManager.removeItem(at: file)
    }

    public func removeOtherRevisions(forPasteID pasteID: String, keeping revisionID: String) throws {
        try validateCanonicalUUID(pasteID)
        try validateCanonicalUUID(revisionID)

        let pasteDirectory = directoryURL(forPasteID: pasteID)
        guard try ensureExistingDirectory(pasteDirectory) else { return }
        let entries = try fileManager.contentsOfDirectory(
            at: pasteDirectory,
            includingPropertiesForKeys: [.isDirectoryKey, .isSymbolicLinkKey],
            options: []
        )
        for entry in entries {
            if entry.lastPathComponent == revisionID {
                guard Self.fileType(at: entry, fileManager: fileManager) == .typeDirectory else {
                    throw AttachmentCacheError.invalidEntry
                }
            } else {
                try fileManager.removeItem(at: entry)
            }
        }
    }

    public func removeAll(forPasteID pasteID: String) throws {
        try validateCanonicalUUID(pasteID)
        let pasteDirectory = directoryURL(forPasteID: pasteID)
        guard try ensureExistingDirectory(pasteDirectory) else { return }
        try fileManager.removeItem(at: pasteDirectory)
    }

    private func fileURL(for key: Key) -> URL {
        directoryURL(forPasteID: key.pasteID)
            .appendingPathComponent(key.revisionID, isDirectory: true)
            .appendingPathComponent(String(key.assetIndex), isDirectory: false)
    }

    private func directoryURL(forPasteID pasteID: String) -> URL {
        root.appendingPathComponent(pasteID, isDirectory: true)
    }

    private func ensureDirectory(_ directory: URL) throws {
        if let type = Self.fileType(at: directory, fileManager: fileManager) {
            guard type == .typeDirectory else { throw AttachmentCacheError.invalidEntry }
            return
        }
        try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
        guard Self.fileType(at: directory, fileManager: fileManager) == .typeDirectory else {
            throw AttachmentCacheError.invalidEntry
        }
    }

    private func ensureExistingDirectory(_ directory: URL) throws -> Bool {
        guard let type = Self.fileType(at: directory, fileManager: fileManager) else { return false }
        guard type == .typeDirectory else { throw AttachmentCacheError.invalidEntry }
        return true
    }

    private func validateCanonicalUUID(_ value: String) throws {
        guard Self.isCanonicalUUID(value) else { throw AttachmentCacheError.invalidIdentifier }
    }

    private static func isCanonicalUUID(_ value: String) -> Bool {
        let bytes = Array(value.utf8)
        guard bytes.count == 36 else { return false }
        let hyphenPositions: Set<Int> = [8, 13, 18, 23]
        for (index, byte) in bytes.enumerated() {
            if hyphenPositions.contains(index) {
                guard byte == 45 else { return false }
            } else {
                guard (byte >= 48 && byte <= 57) || (byte >= 97 && byte <= 102) else { return false }
            }
        }
        return UUID(uuidString: value) != nil
    }

    private static func fileType(at url: URL, fileManager: FileManager) -> FileAttributeType? {
        guard let attributes = try? fileManager.attributesOfItem(atPath: url.path) else { return nil }
        return attributes[.type] as? FileAttributeType
    }
}
