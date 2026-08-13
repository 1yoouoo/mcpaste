import Foundation

public final class WorkspaceSession {
    public private(set) var syncState: SyncState = .idle
    public private(set) var history: [CachedPaste] = []
    public private(set) var pendingCount = 0
    public private(set) var lastMCPAccessAt: Date?
    public private(set) var devices: [DeviceRecord] = []

    private let cache: SQLiteCache
    private let coordinator: SyncCoordinator
    private let queue: OfflineMutationQueue
    private let api: MCPasteAPI?
    public init(cache: SQLiteCache, coordinator: SyncCoordinator, api: MCPasteAPI? = nil) { self.cache = cache; self.coordinator = coordinator; self.queue = OfflineMutationQueue(cache: cache); self.api = api; self.history = (try? cache.allPastes()) ?? [] }

    public func refresh() async throws {
        try await coordinator.sync()
        syncState = coordinator.state
        history = (try? cache.allPastes()) ?? []
        pendingCount = (try? queue.pending().count) ?? 0
    }

    public func refreshDevices() async throws {
        guard let api else { return }
        devices = try await api.listDevices()
    }

    public func renameDevice(id: String, displayName: String) async throws {
        guard let api else { return }
        _ = try await api.renameDevice(id: id, displayName: displayName)
        try await refreshDevices()
    }

    public func revokeDevice(id: String) async throws {
        guard let api else { return }
        try await api.revokeDevice(id: id)
        try await refreshDevices()
    }

    public func enqueue(_ mutation: MutationKind, pasteID: String, body: Data) throws {
        _ = try queue.enqueue(kind: mutation, pasteID: pasteID, body: body)
        pendingCount = (try? queue.pending().count) ?? pendingCount
    }

    public func replayPending() async {
        guard let api else { return }
        guard let pending = try? queue.pending() else { return }
        for item in pending {
            do {
                switch item.kind {
                case .create:
                    let request = try JSONDecoder().decode(CreatePasteRequest.self, from: item.body)
                    _ = try await api.createPaste(text: request.text, idempotencyKey: item.idempotencyKey)
                case .update:
                    let request = try JSONDecoder().decode(CreatePasteRequest.self, from: item.body)
                    _ = try await api.updatePaste(id: item.pasteID, text: request.text, idempotencyKey: item.idempotencyKey)
                case .delete:
                    try await api.deletePaste(id: item.pasteID, idempotencyKey: item.idempotencyKey)
                }
                try? queue.remove(item.id)
            } catch {
                _ = try? queue.retry(item.id)
                break
            }
        }
        pendingCount = (try? queue.pending().count) ?? pendingCount
    }

    public func startPolling() { coordinator.startPolling() }
    public func stopPolling() { coordinator.stopPolling() }

    public func markMCPAccess(at date: Date = Date()) { lastMCPAccessAt = date }
}
