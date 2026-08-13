import Foundation
import MCPasteCore

public enum AppScreen: Equatable { case onboarding, pasteboard }

@MainActor
public final class AppModel: ObservableObject {
    @Published public private(set) var screen: AppScreen = .onboarding
    @Published public var draft = ""
    @Published public var endpoint = ""
    @Published public var deviceName = Host.current().localizedName ?? "Mac"
    @Published public var pairingID = ""
    @Published public var claimSecret = ""
    @Published public var recoveryCode = ""
    @Published public private(set) var history: [CachedPaste] = []
    @Published public private(set) var devices: [DeviceRecord] = []
    @Published public private(set) var pending = false
    @Published public private(set) var pendingCount = 0
    @Published public private(set) var errorMessage: String?

    private var api: MCPasteAPI?
    private var currentPasteID: String?
    private let keychain: KeychainStore
    private var session: WorkspaceSession?
    private var pollingTask: Task<Void, Never>?
    public init() { self.keychain = KeychainStore(); Task { await restore() } }
    public init(keychain: KeychainStore) { self.keychain = keychain }
    deinit { pollingTask?.cancel() }

    public func createWorkspace() async {
        guard let url = URL(string: endpoint), url.scheme == "https", !url.host().isNilOrEmpty else { errorMessage = "Enter a valid HTTPS server address."; return }
        pending = true
        defer { pending = false }
        do {
            let bootstrap = MCPasteAPI(baseURL: url, token: "")
            let grant = try await bootstrap.createWorkspace(deviceName: deviceName)
            guard let credential = grant.credentials.first(where: { $0.kind == "full" }) else { throw APIError.invalidResponse }
            recoveryCode = grant.recoveryCode ?? ""
            try await install(grant: grant, endpoint: url, token: credential.token)
        } catch { errorMessage = "Workspace setup failed." }
    }

    public func recoverWorkspace() async {
        guard let url = URL(string: endpoint), url.scheme == "https", !url.host().isNilOrEmpty, !recoveryCode.isEmpty else { errorMessage = "Enter the HTTPS endpoint and recovery code."; return }
        pending = true; defer { pending = false }
        do {
            let grant = try await MCPasteAPI(baseURL: url, token: "").recoverWorkspace(recoveryCode: recoveryCode, deviceName: deviceName)
            guard let credential = grant.credentials.first(where: { $0.kind == "full" }) else { throw APIError.invalidResponse }
            recoveryCode = grant.recoveryCode ?? ""
            try await install(grant: grant, endpoint: url, token: credential.token)
        } catch { errorMessage = "Workspace recovery failed." }
    }

    public func joinWorkspace() async {
        guard let url = URL(string: endpoint), url.scheme == "https", !url.host().isNilOrEmpty, !pairingID.isEmpty, !claimSecret.isEmpty else { errorMessage = "Enter the HTTPS endpoint, pairing ID, and claim secret."; return }
        pending = true; defer { pending = false }
        do {
            let grant = try await MCPasteAPI(baseURL: url, token: "").claimPairing(pairingID: pairingID, claimSecret: claimSecret)
            guard let credential = grant.credentials.first(where: { $0.kind == "full" }) else { throw APIError.invalidResponse }
            try await install(grant: grant, endpoint: url, token: credential.token)
        } catch { errorMessage = "Workspace join failed." }
    }

    public func restore() async {
        guard api == nil else { return }
        do {
            guard let credential = try keychain.loadAll().first(where: { $0.scope == "full" }), let endpoint = credential.endpoint, let url = URL(string: endpoint) else { return }
            try await installSession(workspaceID: credential.workspaceID, deviceID: credential.deviceID, endpoint: url, token: credential.token)
            screen = .pasteboard
            await session?.replayPending()
            try await refreshSession()
        } catch { errorMessage = "Saved workspace could not be restored." }
    }

    public func saveDraft() async {
        guard let api else { return }
        pending = true; defer { pending = false }
        do {
            let record = if let currentPasteID { try await api.updatePaste(id: currentPasteID, text: draft) } else { try await api.createPaste(text: draft) }
            currentPasteID = record.id
            try await refreshSession()
            pending = false
        } catch APIError.transport {
            do { try enqueueDraft(kind: currentPasteID == nil ? .create : .update); pendingCount = session?.pendingCount ?? 0 } catch { errorMessage = "Paste could not be queued."; pending = false }
        } catch { errorMessage = "Paste could not be saved."; pending = false }
    }

    public func deleteCurrentPaste() async {
        guard let api, let currentPasteID else { return }
        pending = true
        do { try await api.deletePaste(id: currentPasteID); self.currentPasteID = nil; draft = ""; try await refreshSession(); pending = false } catch APIError.transport { do { try session?.enqueue(.delete, pasteID: currentPasteID, body: Data()); pendingCount = session?.pendingCount ?? 0 } catch { errorMessage = "Paste could not be queued."; pending = false } } catch { errorMessage = "Paste could not be deleted."; pending = false }
    }

    public func uploadImages(_ sourceData: [Data]) async {
        guard let api else { return }
        pending = true; defer { pending = false }
        do {
            guard sourceData.count <= ImageNormalizer.maxBundleItems else { throw ImageNormalizationError.tooLarge }
            let normalizer = ImageNormalizer()
            let normalized = try sourceData.map { try normalizer.normalize($0) }
            guard normalized.reduce(0, { $0 + $1.data.count }) <= ImageNormalizer.maxBundleBytes else { throw ImageNormalizationError.tooLarge }
            let record = try await api.uploadImages(normalized)
            currentPasteID = record.id
            try await refreshSession()
        } catch { errorMessage = "Images could not be uploaded." }
    }

    public func selectPaste(_ paste: CachedPaste) {
        guard !paste.deleted, let text = paste.text else { return }
        currentPasteID = paste.id
        draft = text
    }

    public func renameDevice(id: String, displayName: String) async {
        do { try await session?.renameDevice(id: id, displayName: displayName); devices = session?.devices ?? [] } catch { errorMessage = "Device could not be renamed." }
    }

    public func revokeDevice(id: String) async {
        do { try await session?.revokeDevice(id: id); devices = session?.devices ?? [] } catch { errorMessage = "Device could not be revoked." }
    }

    private func enqueueDraft(kind: MutationKind) throws {
        let body = try JSONEncoder().encode(CreatePasteRequest(text: draft))
        try session?.enqueue(kind, pasteID: currentPasteID ?? "new", body: body)
    }

    private func install(grant: WorkspaceGrant, endpoint: URL, token: String) async throws {
        try keychain.save(KeychainCredential(workspaceID: grant.workspaceID, deviceID: grant.deviceID, scope: "full", endpoint: endpoint.absoluteString, token: token))
        try await installSession(workspaceID: grant.workspaceID, deviceID: grant.deviceID, endpoint: endpoint, token: token)
        try await refreshSession()
        screen = .pasteboard
    }

    private func installSession(workspaceID: String, deviceID: String, endpoint: URL, token: String) async throws {
        let client = MCPasteAPI(baseURL: endpoint, token: token)
        let cacheDirectory = try FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true).appendingPathComponent("MCPaste", isDirectory: true)
        try FileManager.default.createDirectory(at: cacheDirectory, withIntermediateDirectories: true)
        let cache = try SQLiteCache(path: cacheDirectory.appendingPathComponent("\(workspaceID).sqlite").path)
        let coordinator = SyncCoordinator(api: client, cache: cache)
        api = client
        session = WorkspaceSession(cache: cache, coordinator: coordinator, api: client)
        pollingTask?.cancel()
        pollingTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self, !Task.isCancelled else { return }
                if let api = self.api, let session = self.session {
                    do {
                        for try await _ in api.eventStream(after: session.currentCursor()) {
                            try? await self.refreshSession()
                        }
                    } catch {
                        try? await self.refreshSession()
                    }
                }
                do { try await Task.sleep(nanoseconds: 15 * 1_000_000_000) } catch { return }
            }
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
    }

    public func completeOnboarding() { screen = .pasteboard }
    public func setPending(_ value: Bool) { pending = value }
}

private extension Optional where Wrapped == String {
    var isNilOrEmpty: Bool { self?.isEmpty ?? true }
}
