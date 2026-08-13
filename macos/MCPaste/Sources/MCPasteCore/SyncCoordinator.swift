import Foundation

public final class SyncCoordinator {
    private let api: SyncAPI
    private let cache: SQLiteCache
    private var pollingTask: Task<Void, Never>?
    public private(set) var state: SyncState = .idle
    public init(api: SyncAPI, cache: SQLiteCache) { self.api = api; self.cache = cache }

    deinit { pollingTask?.cancel() }

    public func startPolling(every seconds: UInt64 = 15) {
        pollingTask?.cancel()
        pollingTask = Task { [weak self] in
            while !Task.isCancelled {
                do { try await Task.sleep(nanoseconds: seconds * 1_000_000_000) } catch { return }
                guard let self, !Task.isCancelled else { return }
                try? await self.sync()
            }
        }
    }

    public func stopPolling() { pollingTask?.cancel(); pollingTask = nil }

    public func sync() async throws {
        state = .syncing
        var didResetCursor = false
        do {
            var after = try cache.cursor()
            repeat {
                let page: SyncPage
                do {
                    page = try await api.sync(after: after)
                } catch APIError.cursorExpired where !didResetCursor {
                    didResetCursor = true
                    if let snapshotAPI = api as? SnapshotAPI {
                        let snapshot = try await snapshotAPI.snapshot()
                        for paste in snapshot.pastes {
                            try cache.savePaste(CachedPaste(id: paste.id, revisionID: paste.revisionID, sequence: paste.sequence, text: paste.text, deleted: paste.deleted, expiresAt: paste.expiresAt))
                        }
                        try cache.setCursor(snapshot.cursor)
                        state = .idle
                        return
                    }
                    after = 0
                    continue
                }
                for event in page.events.sorted(by: { $0.sequence < $1.sequence }) {
                    try cache.savePaste(CachedPaste(id: event.pasteID, revisionID: event.revisionID, sequence: event.sequence, text: event.deleted ? nil : event.text, deleted: event.deleted, expiresAt: Date().addingTimeInterval(3600)))
                    after = max(after, event.sequence)
                }
                try cache.setCursor(page.hasMore ? after : max(page.cursor, after))
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
