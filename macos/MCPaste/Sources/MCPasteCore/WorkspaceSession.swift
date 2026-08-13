import Foundation

public final class WorkspaceSession {
    public private(set) var syncState: SyncState = .idle
    public private(set) var history: [CachedPaste] = []
    public private(set) var pendingCount = 0
    public private(set) var lastMCPAccessAt: Date?

    private let cache: SQLiteCache
    private let coordinator: SyncCoordinator
    public init(cache: SQLiteCache, coordinator: SyncCoordinator) { self.cache = cache; self.coordinator = coordinator }

    public func refresh() async throws {
        try await coordinator.sync()
        syncState = coordinator.state
        pendingCount = 0
    }

    public func markMCPAccess(at date: Date = Date()) { lastMCPAccessAt = date }
}
