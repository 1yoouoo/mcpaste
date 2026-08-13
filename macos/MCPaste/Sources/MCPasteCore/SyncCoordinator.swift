import Foundation

public final class SyncCoordinator {
    private let api: SyncAPI
    private let cache: SQLiteCache
    public private(set) var state: SyncState = .idle
    public init(api: SyncAPI, cache: SQLiteCache) { self.api = api; self.cache = cache }

    public func sync() async throws {
        state = .syncing
        do {
            var after = try cache.cursor()
            repeat {
                let page = try await api.sync(after: after)
                for event in page.events.sorted(by: { $0.sequence < $1.sequence }) {
                    try cache.savePaste(CachedPaste(id: event.pasteID, revisionID: event.revisionID, sequence: event.sequence, text: event.deleted ? nil : event.text, deleted: event.deleted, expiresAt: Date().addingTimeInterval(3600)))
                    after = max(after, event.sequence)
                }
                try cache.setCursor(max(page.cursor, after))
                if !page.hasMore { break }
            } while true
            state = .idle
        } catch {
            state = .failed
            throw error
        }
    }
}

public enum SyncState: Equatable { case idle, syncing, failed }
