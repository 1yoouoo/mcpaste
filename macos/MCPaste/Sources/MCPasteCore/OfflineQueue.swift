import Foundation

public final class OfflineMutationQueue {
    private let cache: SQLiteCache
    public init(cache: SQLiteCache) { self.cache = cache }
    public func enqueue(kind: MutationKind, pasteID: String, body: Data, idempotencyKey: String? = nil) throws -> PendingMutation { try cache.enqueue(kind: kind, pasteID: pasteID, body: body, idempotencyKey: idempotencyKey) }
    public func enqueueAttachments(pasteID: String, manifest: PendingAttachmentManifest, idempotencyKey: String? = nil) throws -> PendingMutation {
        try cache.enqueue(kind: .replaceAttachments, pasteID: pasteID, body: JSONEncoder().encode(manifest), idempotencyKey: idempotencyKey)
    }
    public func pending() throws -> [PendingMutation] { try cache.pending() }
    public func retry(_ id: Int) throws -> PendingMutation { try cache.retry(id) }
    public func remove(_ id: Int) throws { try cache.remove(id) }
}
