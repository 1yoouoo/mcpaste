import Foundation

public final class OfflineMutationQueue {
    private let cache: SQLiteCache
    public init(cache: SQLiteCache) { self.cache = cache }
    public func enqueue(kind: MutationKind, pasteID: String, body: Data) throws -> PendingMutation { try cache.enqueue(kind: kind, pasteID: pasteID, body: body) }
    public func pending() throws -> [PendingMutation] { try cache.pending() }
    public func retry(_ id: Int) throws -> PendingMutation { try cache.retry(id) }
}
