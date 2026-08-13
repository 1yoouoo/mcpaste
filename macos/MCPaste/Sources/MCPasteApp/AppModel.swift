import Foundation
import MCPasteCore

public enum AppScreen: Equatable { case onboarding, pasteboard }

@MainActor
public final class AppModel: ObservableObject {
    @Published public private(set) var screen: AppScreen = .onboarding
    @Published public var draft = ""
    @Published public var endpoint = ""
    @Published public var deviceName = Host.current().localizedName ?? "Mac"
    @Published public private(set) var pending = false
    @Published public private(set) var errorMessage: String?

    private var api: MCPasteAPI?
    private var currentPasteID: String?
    private let keychain: KeychainStore
    public init() { self.keychain = KeychainStore() }
    public init(keychain: KeychainStore) { self.keychain = keychain }

    public func createWorkspace() async {
        guard let url = URL(string: endpoint), url.scheme == "https", !url.host().isNilOrEmpty else { errorMessage = "Enter a valid HTTPS server address."; return }
        pending = true; defer { pending = false }
        do {
            let bootstrap = MCPasteAPI(baseURL: url, token: "")
            let grant = try await bootstrap.createWorkspace(deviceName: deviceName)
            guard let credential = grant.credentials.first(where: { $0.kind == "full" }) else { throw APIError.invalidResponse }
            try keychain.save(KeychainCredential(workspaceID: grant.workspaceID, deviceID: grant.deviceID, scope: "full", token: credential.token))
            api = MCPasteAPI(baseURL: url, token: credential.token)
            screen = .pasteboard
        } catch { errorMessage = "Workspace setup failed." }
    }

    public func saveDraft() async {
        guard let api else { return }
        pending = true; defer { pending = false }
        do {
            let record = if let currentPasteID { try await api.updatePaste(id: currentPasteID, text: draft) } else { try await api.createPaste(text: draft) }
            currentPasteID = record.id
        } catch { errorMessage = "Paste could not be saved." }
    }

    public func deleteCurrentPaste() async {
        guard let api, let currentPasteID else { return }
        pending = true; defer { pending = false }
        do { try await api.deletePaste(id: currentPasteID); self.currentPasteID = nil; draft = "" } catch { errorMessage = "Paste could not be deleted." }
    }

    public func completeOnboarding() { screen = .pasteboard }
    public func setPending(_ value: Bool) { pending = value }
}

private extension Optional where Wrapped == String {
    var isNilOrEmpty: Bool { self?.isEmpty ?? true }
}
