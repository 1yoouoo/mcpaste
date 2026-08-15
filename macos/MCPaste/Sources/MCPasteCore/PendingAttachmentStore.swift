import Foundation

private enum PendingAttachmentStoreError: Error {
    case invalidRoot
    case invalidPath
    case invalidEntry
}

public struct PendingAttachmentManifest: Codable, Equatable {
    public struct Item: Codable, Equatable {
        public let path: String
        public let mimeType: String
        public let width: Int
        public let height: Int

        public init(path: String, mimeType: String, width: Int, height: Int) {
            self.path = path; self.mimeType = mimeType; self.width = width; self.height = height
        }
    }

    public let items: [Item]
    public init(items: [Item]) { self.items = items }
}

public final class PendingAttachmentStore {
    private let fileManager: FileManager
    private let root: URL

    public init(root: URL) throws {
        let fileManager = FileManager.default
        let standardizedRoot = root.standardizedFileURL
        if let type = Self.fileType(at: standardizedRoot, fileManager: fileManager) {
            guard type == .typeDirectory else { throw PendingAttachmentStoreError.invalidRoot }
        } else {
            try fileManager.createDirectory(at: standardizedRoot, withIntermediateDirectories: true)
            guard Self.fileType(at: standardizedRoot, fileManager: fileManager) == .typeDirectory else {
                throw PendingAttachmentStoreError.invalidRoot
            }
        }
        self.fileManager = fileManager
        self.root = standardizedRoot
    }

    public func persist(_ images: [NormalizedImage]) throws -> PendingAttachmentManifest {
        var items: [PendingAttachmentManifest.Item] = []
        do {
            for image in images {
                let file = root.appendingPathComponent(UUID().uuidString.lowercased(), isDirectory: false)
                try image.data.write(to: file, options: .atomic)
                items.append(.init(path: file.path, mimeType: image.mimeType, width: image.width, height: image.height))
            }
            return PendingAttachmentManifest(items: items)
        } catch {
            for item in items { try? fileManager.removeItem(atPath: item.path) }
            throw error
        }
    }

    public func load(_ manifest: PendingAttachmentManifest) throws -> [NormalizedImage] {
        try manifest.items.map { item in
            let file = try validatedFileURL(for: item.path)
            guard Self.fileType(at: file, fileManager: fileManager) == .typeRegular else {
                throw PendingAttachmentStoreError.invalidEntry
            }
            return NormalizedImage(
                mimeType: item.mimeType,
                width: item.width,
                height: item.height,
                data: try Data(contentsOf: file)
            )
        }
    }

    public func remove(_ manifest: PendingAttachmentManifest) throws {
        let files = try manifest.items.map { try validatedFileURL(for: $0.path) }
        var firstError: Error?
        for file in files where fileManager.fileExists(atPath: file.path) {
            guard Self.fileType(at: file, fileManager: fileManager) == .typeRegular else {
                throw PendingAttachmentStoreError.invalidEntry
            }
            do {
                try fileManager.removeItem(at: file)
            } catch {
                firstError = firstError ?? error
            }
        }
        if let firstError { throw firstError }
    }

    public func removeOrphans(referencedPaths: Set<String>) throws {
        let referenced = try Set(referencedPaths.map { try validatedFileURL(for: $0).path })
        let entries = try fileManager.contentsOfDirectory(at: root, includingPropertiesForKeys: [], options: [])
        for entry in entries where !referenced.contains(entry.standardizedFileURL.path) {
            guard Self.fileType(at: entry, fileManager: fileManager) == .typeRegular else {
                throw PendingAttachmentStoreError.invalidEntry
            }
            try fileManager.removeItem(at: entry)
        }
    }

    private func validatedFileURL(for path: String) throws -> URL {
        let file = URL(fileURLWithPath: path).standardizedFileURL
        guard file.deletingLastPathComponent().path == root.path,
              Self.isCanonicalUUID(file.lastPathComponent) else {
            throw PendingAttachmentStoreError.invalidPath
        }
        return file
    }

    private static func fileType(at url: URL, fileManager: FileManager) -> FileAttributeType? {
        guard let attributes = try? fileManager.attributesOfItem(atPath: url.path) else { return nil }
        return attributes[.type] as? FileAttributeType
    }

    private static func isCanonicalUUID(_ value: String) -> Bool {
        UUID(uuidString: value)?.uuidString.lowercased() == value
    }
}
