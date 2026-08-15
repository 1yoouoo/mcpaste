import Foundation

public final class SyncCoordinator {
    private let api: SyncAPI
    private let cache: SQLiteCache
    private let syncGate = SyncGate()
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
        await syncGate.acquire()
        do {
            try await syncOnce()
            await syncGate.release()
        } catch {
            await syncGate.release()
            throw error
        }
    }

    private func syncOnce() async throws {
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
                        try cache.replacePastes(snapshot.pastes.map { paste in
                            CachedPaste(
                                id: paste.id,
                                revisionID: paste.revisionID,
                                sequence: paste.sequence,
                                text: paste.text,
                                deleted: paste.deleted,
                                expiresAt: paste.expiresAt,
                                attachmentRevisionID: paste.attachmentRevisionID,
                                attachments: paste.attachments
                            )
                        })
                        try cache.setCursor(snapshot.cursor)
                        after = snapshot.cursor
                        continue
                    }
                    after = 0
                    continue
                }
                for event in page.events.sorted(by: { $0.sequence < $1.sequence }) {
                    try cache.apply(event)
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

private actor SyncGate {
    private var locked = false
    private var waiters: [CheckedContinuation<Void, Never>] = []

    func acquire() async {
        if !locked {
            locked = true
            return
        }
        await withCheckedContinuation { continuation in
            waiters.append(continuation)
        }
    }

    func release() {
        if waiters.isEmpty {
            locked = false
        } else {
            waiters.removeFirst().resume()
        }
    }
}
