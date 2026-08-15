import Foundation
import Security

public struct KeychainCredential: Codable, Equatable {
    public let workspaceID: String
    public let deviceID: String
    public let scope: String
    public let endpoint: String?
    public let token: String
    public init(workspaceID: String, deviceID: String, scope: String = "full", endpoint: String? = nil, token: String) {
        self.workspaceID = workspaceID; self.deviceID = deviceID; self.scope = scope; self.endpoint = endpoint; self.token = token
    }
}

public final class KeychainStore {
    private let service: String
    public init(service: String = "com.mcpaste.credentials") { self.service = service }

    public func save(_ credential: KeychainCredential) throws {
        let data = try JSONEncoder().encode(credential)
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account(credential)]
        let attributes: [String: Any] = [kSecValueData as String: data, kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly]
        let status = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if status == errSecItemNotFound {
            var add = query; for (key, value) in attributes { add[key] = value }
            guard SecItemAdd(add as CFDictionary, nil) == errSecSuccess else { throw KeychainError.operation }
        } else if status != errSecSuccess { throw KeychainError.operation }
    }

    public func load(workspaceID: String, deviceID: String, scope: String = "full") throws -> KeychainCredential? {
        try load(account: account(workspaceID: workspaceID, deviceID: deviceID, scope: scope))
    }

    public func loadAll() throws -> [KeychainCredential] {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecReturnAttributes as String: true,
            kSecMatchLimit as String: kSecMatchLimitAll
        ]
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return [] }
        guard status == errSecSuccess, let values = result as? [[String: Any]] else { throw KeychainError.operation }
        return try values.map { attributes in
            guard let account = attributes[kSecAttrAccount as String] as? String,
                  let credential = try load(account: account) else { throw KeychainError.operation }
            return credential
        }
    }

    private func load(account: String) throws -> KeychainCredential? {
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account, kSecReturnData as String: true, kSecMatchLimit as String: kSecMatchLimitOne]
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data else { throw KeychainError.operation }
        do { return try JSONDecoder().decode(KeychainCredential.self, from: data) } catch { throw KeychainError.operation }
    }

    public func remove(workspaceID: String, deviceID: String, scope: String = "full") throws {
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account(workspaceID: workspaceID, deviceID: deviceID, scope: scope)]
        let status = SecItemDelete(query as CFDictionary)
        if status != errSecSuccess && status != errSecItemNotFound { throw KeychainError.operation }
    }

    private func account(_ credential: KeychainCredential) -> String { account(workspaceID: credential.workspaceID, deviceID: credential.deviceID, scope: credential.scope) }
    private func account(workspaceID: String, deviceID: String, scope: String) -> String { "\(workspaceID):\(deviceID):\(scope)" }
}

public enum KeychainError: Error { case operation }
