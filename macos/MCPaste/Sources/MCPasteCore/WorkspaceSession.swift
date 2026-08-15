import Foundation

@MainActor
public final class WorkspaceSession {
    public private(set) var syncState: SyncState = .idle
    public private(set) var history: [CachedPaste] = []
    public private(set) var pendingCount = 0
    public private(set) var lastMCPAccessAt: Date?
    public private(set) var devices: [DeviceRecord] = []

    private let cache: SQLiteCache
    private let coordinator: SyncCoordinator
    private let queue: OfflineMutationQueue
    private let api: WorkspaceAPI?
    private let deviceAPI: DeviceAPI?
    private let attachmentStore: PendingAttachmentStore?
    private let attachmentCache: AttachmentCache?
    private let replayGate = ReplayGate()
    private var pasteIDRemaps: [String: String] = [:]
    public init(
        cache: SQLiteCache,
        coordinator: SyncCoordinator,
        api: WorkspaceAPI? = nil,
        deviceAPI: DeviceAPI? = nil,
        attachmentStore: PendingAttachmentStore? = nil,
        attachmentCache: AttachmentCache? = nil
    ) {
        self.cache = cache
        self.coordinator = coordinator
        self.queue = OfflineMutationQueue(cache: cache)
        self.api = api
        self.deviceAPI = deviceAPI
        self.attachmentStore = attachmentStore
        self.attachmentCache = attachmentCache
        do {
            self.history = try cache.allPastes()
        } catch {
            self.history = []
            self.syncState = .failed
        }
        cleanupAttachmentOrphans()
    }

    public func refresh() async throws {
        do {
            try await coordinator.sync()
            syncState = coordinator.state
            history = try cache.allPastes()
            pendingCount = try queue.pending().count
        } catch {
            syncState = .failed
            throw error
        }
    }

    public func refreshDevices() async throws {
        guard let deviceAPI else { throw APIError.invalidResponse }
        devices = try await deviceAPI.listDevices()
    }

    public func renameDevice(id: String, displayName: String) async throws {
        guard let deviceAPI else { throw APIError.invalidResponse }
        _ = try await deviceAPI.renameDevice(id: id, displayName: displayName, idempotencyKey: UUID().uuidString.lowercased())
        try await refreshDevices()
    }

    public func revokeDevice(id: String) async throws {
        guard let deviceAPI else { throw APIError.invalidResponse }
        try await deviceAPI.revokeDevice(id: id, idempotencyKey: UUID().uuidString.lowercased())
        try await refreshDevices()
    }

    public func enqueue(_ mutation: MutationKind, pasteID: String, body: Data, idempotencyKey: String? = nil) throws {
        _ = try queue.enqueue(kind: mutation, pasteID: pasteID, body: body, idempotencyKey: idempotencyKey)
        pendingCount = (try? queue.pending().count) ?? pendingCount
    }

    public func enqueueAttachments(_ images: [NormalizedImage], pasteID: String, idempotencyKey: String? = nil) throws {
        guard let attachmentStore else { throw APIError.invalidResponse }
        let manifest = try attachmentStore.persist(images)
        do {
            _ = try queue.enqueueAttachments(pasteID: pasteID, manifest: manifest, idempotencyKey: idempotencyKey)
        } catch {
            try? attachmentStore.remove(manifest)
            throw error
        }
        pendingCount = (try? queue.pending().count) ?? pendingCount
    }

    public func replayPending() async {
        guard await replayGate.begin() else { return }
        await replayPendingOnce()
        await replayGate.end()
    }

    private func replayPendingOnce() async {
        guard let api else {
            syncState = .failed
            return
        }
        guard let pending = try? queue.pending() else {
            syncState = .failed
            return
        }
        var remappedPasteIDs: [String: String] = [:]
        replayLoop: for item in pending {
            let pasteID = remappedPasteIDs[item.pasteID] ?? item.pasteID
            do {
                switch item.kind {
                case .create:
                    let request = try JSONDecoder().decode(CreatePasteRequest.self, from: item.body)
                    let record = try await api.createPaste(text: request.text, idempotencyKey: item.idempotencyKey)
                    if item.pasteID.hasPrefix("local:") {
                        try cache.remapPasteID(from: item.pasteID, to: record.id)
                        remappedPasteIDs[item.pasteID] = record.id
                        pasteIDRemaps[item.pasteID] = record.id
                    }
                case .update:
                    let request = try JSONDecoder().decode(CreatePasteRequest.self, from: item.body)
                    _ = try await api.updatePaste(id: pasteID, text: request.text, idempotencyKey: item.idempotencyKey)
                case .delete:
                    try await api.deletePaste(id: pasteID, idempotencyKey: item.idempotencyKey)
                case .replaceAttachments:
                    guard let attachmentStore else {
                        _ = try? queue.retry(item.id)
                        syncState = .failed
                        break replayLoop
                    }
                    guard let manifest = try? JSONDecoder().decode(PendingAttachmentManifest.self, from: item.body) else { throw APIError.invalidResponse }
                    let images = try attachmentStore.load(manifest)
                    let record = try await api.replaceAttachments(
                        pasteID: pasteID,
                        images: images,
                        idempotencyKey: item.idempotencyKey
                    )
                    try await cacheAttachments(for: record)
                    try queue.remove(item.id)
                    do {
                        try attachmentStore.remove(manifest)
                    } catch {
                        syncState = .failed
                        cleanupAttachmentOrphans()
                        break replayLoop
                    }
                }
                if item.kind != .replaceAttachments {
                    try queue.remove(item.id)
                }
            } catch {
                syncState = .failed
                do {
                    _ = try queue.retry(item.id)
                } catch {
                    syncState = .failed
                }
                break
            }
        }
        do {
            pendingCount = try queue.pending().count
        } catch {
            syncState = .failed
        }
    }

    public func startPolling() { coordinator.startPolling() }
    public func stopPolling() { coordinator.stopPolling() }

    public func currentCursor() -> Int64 { (try? cache.cursor()) ?? 0 }

    public func markMCPAccess(at date: Date = Date()) { lastMCPAccessAt = date }

    public func resolvedPasteID(_ pasteID: String) -> String {
        pasteIDRemaps[pasteID] ?? pasteID
    }

    public func cachedAttachments(for paste: CachedPaste) -> [NormalizedImage] {
        guard let attachmentCache, let revisionID = paste.attachmentRevisionID else { return [] }
        return paste.attachments.sorted { $0.assetIndex < $1.assetIndex }.compactMap { attachment in
            guard let key = try? AttachmentCache.Key(pasteID: paste.id, revisionID: revisionID, assetIndex: attachment.assetIndex),
                  let data = try? attachmentCache.data(for: key) else { return nil }
            return NormalizedImage(mimeType: attachment.mimeType, width: attachment.width, height: attachment.height, data: data)
        }
    }

    public func cacheAttachments(for record: PasteRecord) async throws {
        guard let attachmentCache else { return }
        guard let api else { throw APIError.invalidResponse }
        guard let revisionID = record.attachmentRevisionID else {
            try attachmentCache.removeAll(forPasteID: record.id)
            return
        }

        var storedKeys: [AttachmentCache.Key] = []
        do {
            for attachment in record.attachments {
                let data = try await api.downloadAttachment(pasteID: record.id, assetIndex: attachment.assetIndex)
                let key = try AttachmentCache.Key(
                    pasteID: record.id,
                    revisionID: revisionID,
                    assetIndex: attachment.assetIndex
                )
                try attachmentCache.store(data, for: key)
                storedKeys.append(key)
            }
            try attachmentCache.removeOtherRevisions(forPasteID: record.id, keeping: revisionID)
        } catch {
            for key in storedKeys { try? attachmentCache.remove(key) }
            throw error
        }
    }

    private func cleanupAttachmentOrphans() {
        guard let attachmentStore else { return }
        do {
            let referencedPaths = try queue.pending().reduce(into: Set<String>()) { paths, item in
                guard item.kind == .replaceAttachments else { return }
                let manifest = try JSONDecoder().decode(PendingAttachmentManifest.self, from: item.body)
                paths.formUnion(manifest.items.map(\.path))
            }
            try attachmentStore.removeOrphans(referencedPaths: referencedPaths)
        } catch {
            syncState = .failed
        }
    }
}

private actor ReplayGate {
    private var active = false

    func begin() -> Bool {
        guard !active else { return false }
        active = true
        return true
    }

    func end() { active = false }
}
