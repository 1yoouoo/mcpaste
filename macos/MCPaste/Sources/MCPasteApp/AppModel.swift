import Foundation
import AppKit
import MCPasteCore

public enum AppScreen: Equatable { case onboarding, recovery, pasteboard }

public enum SyncStatus: Equatable {
    case notConnected
    case realtime
    case polling
}

@MainActor
public final class AppModel: ObservableObject {
    @Published public private(set) var screen: AppScreen = .onboarding
    @Published public var draft = ""
    @Published public var deviceName = Host.current().localizedName ?? "Mac"
    @Published public var pairingID = ""
    @Published public var claimSecret = ""
    @Published public var recoveryCode = ""
    @Published public private(set) var history: [CachedPaste] = []
    @Published public private(set) var devices: [DeviceRecord] = []
    @Published public private(set) var pending = false
    @Published public private(set) var pendingCount = 0
    @Published public private(set) var errorMessage: String?
    @Published public private(set) var syncStatus: SyncStatus = .notConnected
    @Published public private(set) var workspaceID: String?
    @Published public private(set) var lastSyncedAt: Date?
    @Published public private(set) var attachments: [NormalizedImage] = []
    /// Background sync health. Kept apart from `errorMessage` so a failed user action never reads as a dropped connection.
    @Published public private(set) var syncFailed = false
    /// Images accepted but not yet stored, so the window can show them arriving.
    @Published public private(set) var uploadingCount = 0

    public var isUploading: Bool { uploadingCount > 0 }

    func beginUpload(count: Int) { uploadingCount += max(0, count) }
    func finishUpload(count: Int = 0) {
        uploadingCount = count > 0 ? max(0, uploadingCount - count) : 0
    }

    public var canDeleteCurrentPaste: Bool { currentPasteID != nil }
    /// Text or images the store has not seen yet. Whitespace on its own is not a paste.
    public var hasUnsavedWork: Bool {
        guard draft != savedDraft || !attachments.isEmpty else { return false }
        return !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !attachments.isEmpty
    }

    func markDraftSaved() { savedDraft = draft }
    public var selectedPasteID: String? { currentPasteID }
    public var selectedPaste: CachedPaste? {
        guard let currentPasteID else { return nil }
        let resolvedID = session?.resolvedPasteID(currentPasteID) ?? currentPasteID
        return history.first { $0.id == resolvedID }
    }

    private var api: MCPasteAPI?
    private var savedDraft = ""
    private let attachmentGate = SerialGate()
    @Published private var currentPasteID: String?
    private let keychain: KeychainStore
    private var session: WorkspaceSession?
    private var pollingTask: Task<Void, Never>?
    public init() { self.keychain = KeychainStore(); Task { await restore() } }
    public init(keychain: KeychainStore, restoreOnInit: Bool = false) {
        self.keychain = keychain
        if restoreOnInit { Task { await restore() } }
    }
    deinit { pollingTask?.cancel() }

    public func createWorkspace() async {
        guard let url = try? MCPasteEndpoint.baseURL() else { errorMessage = "Workspace setup is unavailable."; return }
        pending = true
        defer { pending = false }
        do {
            let bootstrap = MCPasteAPI(baseURL: url, token: "")
            let grant = try await bootstrap.createWorkspace(deviceName: deviceName)
            guard let credential = grant.credentials.first(where: { $0.kind == "full" }) else { throw APIError.invalidResponse }
            recoveryCode = grant.recoveryCode ?? ""
            guard !recoveryCode.isEmpty else { throw APIError.invalidResponse }
            try await install(grant: grant, token: credential.token, showsRecovery: true)
        } catch { errorMessage = "Workspace setup failed." }
    }

    public func recoverWorkspace() async {
        guard let url = try? MCPasteEndpoint.baseURL(), !recoveryCode.isEmpty else { errorMessage = "Enter a recovery code."; return }
        pending = true; defer { pending = false }
        do {
            let grant = try await MCPasteAPI(baseURL: url, token: "").recoverWorkspace(recoveryCode: recoveryCode, deviceName: deviceName)
            guard let credential = grant.credentials.first(where: { $0.kind == "full" }) else { throw APIError.invalidResponse }
            recoveryCode = grant.recoveryCode ?? ""
            guard !recoveryCode.isEmpty else { throw APIError.invalidResponse }
            try await install(grant: grant, token: credential.token, showsRecovery: true)
        } catch { errorMessage = "Workspace recovery failed." }
    }

    public func joinWorkspace() async {
        guard let url = try? MCPasteEndpoint.baseURL(), !pairingID.isEmpty, !claimSecret.isEmpty else { errorMessage = "Enter a pairing ID and claim secret."; return }
        pending = true; defer { pending = false }
        do {
            let grant = try await MCPasteAPI(baseURL: url, token: "").claimPairing(pairingID: pairingID, claimSecret: claimSecret)
            guard let credential = grant.credentials.first(where: { $0.kind == "full" }) else { throw APIError.invalidResponse }
            try await install(grant: grant, token: credential.token)
        } catch { errorMessage = "Workspace join failed." }
    }

    public func restore() async {
        guard api == nil else { return }
        do {
            guard let credential = try keychain.loadAll().first(where: { $0.scope == "full" }) else { return }
            guard MCPasteEndpoint.matchesConfiguredEndpoint(credential.endpoint), let url = try? MCPasteEndpoint.baseURL() else {
                errorMessage = "Saved workspace could not be restored."
                return
            }
            try await installSession(workspaceID: credential.workspaceID, deviceID: credential.deviceID, endpoint: url, token: credential.token)
            screen = .pasteboard
            await session?.replayPending()
            try await refreshSession()
        } catch { errorMessage = "Saved workspace could not be restored." }
    }

    public func saveDraft() async {
        guard let api else { return }
        pending = true; defer { pending = false }
        let idempotencyKey = UUID().uuidString.lowercased()
        do {
            let record = if let currentPasteID {
                try await api.updatePaste(id: session?.resolvedPasteID(currentPasteID) ?? currentPasteID, text: draft, idempotencyKey: idempotencyKey)
            } else {
                try await api.createPaste(text: draft, idempotencyKey: idempotencyKey)
            }
            currentPasteID = record.id
            markDraftSaved()
            try await refreshSession()
            pending = false
        } catch APIError.transport {
            do {
                try enqueueDraft(kind: currentPasteID == nil ? .create : .update, idempotencyKey: idempotencyKey)
                pendingCount = session?.pendingCount ?? 0
            } catch { errorMessage = "Paste could not be queued."; pending = false }
        } catch { errorMessage = "Paste could not be saved."; pending = false }
    }

    public func deleteCurrentPaste() async {
        guard let api, let currentPasteID else { return }
        pending = true
        let idempotencyKey = UUID().uuidString.lowercased()
        let requestPasteID = session?.resolvedPasteID(currentPasteID) ?? currentPasteID
        do {
            try await api.deletePaste(id: requestPasteID, idempotencyKey: idempotencyKey)
            self.currentPasteID = nil
            draft = ""
            savedDraft = ""
            attachments = []
            try await refreshSession()
            pending = false
        } catch APIError.transport {
            do {
                guard let session else { throw APIError.invalidResponse }
                try session.enqueue(.delete, pasteID: currentPasteID, body: Data(), idempotencyKey: idempotencyKey)
                pendingCount = session.pendingCount
            } catch { errorMessage = "Paste could not be queued."; pending = false }
        } catch { errorMessage = "Paste could not be deleted."; pending = false }
    }

    public static func mergedAttachments(existing: [NormalizedImage], incoming: [NormalizedImage]) throws -> [NormalizedImage] {
        let merged = existing + incoming
        try ImageNormalizer.validateAttachmentCount(merged.count)
        return merged
    }

    public func uploadImages(_ sourceData: [Data]) async {
        guard !sourceData.isEmpty else { return }
        errorMessage = nil
        beginUpload(count: sourceData.count)
        await serializeAttachmentWork { [weak self] in
            await self?.performUpload(sourceData)
        }
        finishUpload(count: sourceData.count)
    }

    private func performUpload(_ sourceData: [Data]) async {
        do {
            // Normalising megapixel images is CPU work; keeping it off the main actor
            // leaves the window responsive while the thumbnails show them arriving.
            let normalized = try await Self.normalize(sourceData)
            await applyAttachments(try Self.mergedAttachments(existing: attachments, incoming: normalized))
        } catch { errorMessage = Self.attachmentFailureMessage(for: error) }
    }

    private static func normalize(_ sourceData: [Data]) async throws -> [NormalizedImage] {
        try await Task.detached(priority: .userInitiated) {
            let normalizer = ImageNormalizer()
            return try sourceData.map { try normalizer.normalize($0) }
        }.value
    }

    public func removeAttachment(at index: Int) async {
        guard attachments.indices.contains(index) else { return }
        await applyAttachments(attachments.enumerated().filter { $0.offset != index }.map(\.element))
    }

    private func applyAttachments(_ normalized: [NormalizedImage]) async {
        guard let api, let session else { return }
        pending = true; defer { pending = false }
        do {
            guard normalized.reduce(0, { $0 + $1.data.count }) <= ImageNormalizer.maxBundleBytes else { throw ImageNormalizationError.tooLarge }
            let pasteID: String
            if let currentPasteID {
                pasteID = session.resolvedPasteID(currentPasteID)
            } else {
                let createKey = UUID().uuidString.lowercased()
                do {
                    let record = try await api.createPaste(text: draft, idempotencyKey: createKey)
                    currentPasteID = record.id
                    pasteID = record.id
                } catch APIError.transport {
                    let localID = "local:\(UUID().uuidString.lowercased())"
                    let body = try JSONEncoder().encode(CreatePasteRequest(text: draft))
                    try session.enqueue(.create, pasteID: localID, body: body, idempotencyKey: createKey)
                    try session.enqueueAttachments(normalized, pasteID: localID, idempotencyKey: UUID().uuidString.lowercased())
                    currentPasteID = localID
                    attachments = normalized
                    pendingCount = session.pendingCount
                    return
                }
            }

            let attachmentKey = UUID().uuidString.lowercased()
            do {
                let record = try await api.replaceAttachments(pasteID: pasteID, images: normalized, idempotencyKey: attachmentKey)
                currentPasteID = record.id
                try await session.cacheAttachments(for: record)
                attachments = normalized
                try await refreshSession()
            } catch APIError.transport {
                try session.enqueueAttachments(normalized, pasteID: pasteID, idempotencyKey: attachmentKey)
                attachments = normalized
                pendingCount = session.pendingCount
            }
        } catch { errorMessage = Self.attachmentFailureMessage(for: error) }
    }

    static func attachmentFailureMessage(for error: Error) -> String {
        switch error {
        case ImageNormalizationError.tooLarge:
            return "A paste holds up to \(ImageNormalizer.maxAttachmentItems) images and 32 MB in total."
        case ImageNormalizationError.animated:
            return "Animated images are not supported."
        case ImageNormalizationError.unsupported, ImageNormalizationError.malformed:
            return "That image format is not supported."
        case ImageNormalizationError.encodingFailed:
            return "That image could not be prepared for upload."
        case APIError.unauthorized:
            return "This device is no longer signed in to the workspace."
        case APIError.forbidden:
            return "This device is not allowed to change pastes."
        case APIError.notFound:
            // The API returns the same not_found body for a missing paste and for an unrouted path,
            // so this names both causes instead of guessing one.
            return "The server did not accept the images (HTTP 404) — the paste is gone, or this server has no attachment endpoint."
        case APIError.conflict:
            return "Another change to this paste is still in flight. Try again."
        case APIError.invalidResponse:
            return "The server sent a response the app could not read."
        case APIError.http(let status):
            return "The server rejected the images (HTTP \(status))."
        default:
            return "Images could not be uploaded."
        }
    }

    public func selectPaste(_ paste: CachedPaste) {
        guard !paste.deleted else { return }
        currentPasteID = paste.id
        draft = paste.text ?? ""
        savedDraft = draft
        attachments = session?.cachedAttachments(for: paste) ?? []
    }

    /// Commits whatever is on screen. Called before a new paste begins, when the window
    /// closes, and on quit, so work is never lost without a Save button.
    public func saveIfNeeded() async {
        guard hasUnsavedWork else { return }
        await saveDraft()
    }

    /// Opening a new paste commits whatever is on screen, so nothing is lost without a Save button.
    public func startNewPaste() async {
        await saveIfNeeded()
        currentPasteID = nil
        draft = ""
        savedDraft = ""
        attachments = []
    }

    public func renameDevice(id: String, displayName: String) async {
        do { try await session?.renameDevice(id: id, displayName: displayName); devices = session?.devices ?? [] } catch { errorMessage = "Device could not be renamed." }
    }

    public func revokeDevice(id: String) async {
        do { try await session?.revokeDevice(id: id); devices = session?.devices ?? [] } catch { errorMessage = "Device could not be revoked." }
    }

    private func enqueueDraft(kind: MutationKind, idempotencyKey: String) throws {
        guard let session else { throw APIError.invalidResponse }
        let body = try JSONEncoder().encode(CreatePasteRequest(text: draft))
        if currentPasteID == nil { currentPasteID = "local:\(UUID().uuidString.lowercased())" }
        let pasteID = session.resolvedPasteID(currentPasteID!)
        try session.enqueue(kind, pasteID: pasteID, body: body, idempotencyKey: idempotencyKey)
    }

    private func install(grant: WorkspaceGrant, token: String, showsRecovery: Bool = false) async throws {
        let endpoint = try MCPasteEndpoint.baseURL()
        try keychain.save(KeychainCredential(workspaceID: grant.workspaceID, deviceID: grant.deviceID, scope: "full", endpoint: endpoint.absoluteString, token: token))
        try await installSession(workspaceID: grant.workspaceID, deviceID: grant.deviceID, endpoint: endpoint, token: token)
        try await refreshSession()
        screen = showsRecovery ? .recovery : .pasteboard
    }

    private func installSession(workspaceID: String, deviceID: String, endpoint: URL, token: String) async throws {
        let client = MCPasteAPI(baseURL: endpoint, token: token)
        let cacheDirectory = try FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true).appendingPathComponent("MCPaste", isDirectory: true)
        try FileManager.default.createDirectory(at: cacheDirectory, withIntermediateDirectories: true)
        let cache = try SQLiteCache(path: cacheDirectory.appendingPathComponent("\(workspaceID).sqlite").path)
        let coordinator = SyncCoordinator(api: client, cache: cache)
        let attachmentStore = try PendingAttachmentStore(root: cacheDirectory.appendingPathComponent("\(workspaceID)-pending-attachments", isDirectory: true))
        let attachmentCache = try AttachmentCache(root: cacheDirectory.appendingPathComponent("\(workspaceID)-attachment-cache", isDirectory: true))
        api = client
        self.workspaceID = workspaceID
        session = WorkspaceSession(
            cache: cache,
            coordinator: coordinator,
            api: client,
            deviceAPI: client,
            attachmentStore: attachmentStore,
            attachmentCache: attachmentCache
        )
        pollingTask?.cancel()
        pollingTask = Task { @MainActor [weak self] in
            guard let self else { return }
            let loop = RealtimeSyncLoop(
                openEvents: { @MainActor [weak self] in
                    guard let self, let api = self.api, let session = self.session else {
                        throw CancellationError()
                    }
                    for try await _ in api.eventStream(after: session.currentCursor()) {
                        await self.refreshSessionReportingError()
                    }
                },
                refresh: { @MainActor [weak self] in
                    await self?.refreshSessionReportingError()
                },
                onPhase: { @MainActor [weak self] phase in
                    self?.syncStatus = phase == .realtime ? .realtime : .polling
                }
            )
            await loop.run()
        }
        _ = deviceID
    }

	private func refreshSession() async throws {
		await session?.replayPending()
		try await session?.refresh()
        try await session?.refreshDevices()
        history = session?.history ?? []
        pendingCount = session?.pendingCount ?? 0
		devices = session?.devices ?? []
        refreshCurrentAttachments()
        lastSyncedAt = Date()
        syncFailed = false
	}

    private func refreshCurrentAttachments() {
        guard let session, let currentPasteID else { return }
        let resolvedID = session.resolvedPasteID(currentPasteID)
        guard let paste = history.first(where: { $0.id == resolvedID }) else { return }
        applyCachedAttachments(session.cachedAttachments(for: paste), recordedCount: paste.attachments.count)
    }

    /// A cache miss means the bytes have not landed yet, not that the paste lost its images,
    /// so a refresh that arrives mid-upload must not empty the strip.
    func applyCachedAttachments(_ cached: [NormalizedImage], recordedCount: Int) {
        if cached.isEmpty && recordedCount > 0 { return }
        attachments = cached
    }

    /// Attachment writes replace the whole set server-side, so two uploads in flight would
    /// clobber each other. They run one at a time instead.
    func serializeAttachmentWork(_ work: @escaping () async -> Void) async {
        await attachmentGate.run(work)
    }

    private func refreshSessionReportingError() async {
        do {
            try await refreshSession()
        } catch {
            syncFailed = true
        }
    }

    public func retryNow() async {
        errorMessage = nil
        await refreshSessionReportingError()
    }

    public func completeOnboarding() { screen = .pasteboard }
    public func completeRecoverySetup() { screen = .pasteboard }
    public func copyRecoveryCode() {
        guard !recoveryCode.isEmpty else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(recoveryCode, forType: .string)
    }
    public func setPending(_ value: Bool) { pending = value }
}

#if DEBUG
extension AppModel {
    func previewSeed(
        screen: AppScreen = .pasteboard,
        draft: String = "",
        history: [CachedPaste] = [],
        devices: [DeviceRecord] = [],
        pending: Bool = false,
        pendingCount: Int = 0,
        syncStatus: SyncStatus = .realtime,
        workspaceID: String? = nil,
        errorMessage: String? = nil,
        selectedPasteID: String? = nil,
        attachments: [NormalizedImage] = [],
        lastSyncedAt: Date? = nil,
        syncFailed: Bool = false
    ) {
        self.syncFailed = syncFailed
        self.screen = screen
        self.draft = draft
        self.history = history
        self.devices = devices
        self.pending = pending
        self.pendingCount = pendingCount
        self.syncStatus = syncStatus
        self.workspaceID = workspaceID
        self.errorMessage = errorMessage
        self.currentPasteID = selectedPasteID
        self.attachments = attachments
        self.lastSyncedAt = lastSyncedAt
    }
}
#endif
