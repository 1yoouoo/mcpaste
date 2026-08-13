import Foundation

public struct CreatePasteRequest: Codable, Equatable {
    public let text: String
    public init(text: String) { self.text = text }
}

public struct PasteRecord: Codable, Equatable, Identifiable {
    public let id: String
    public let revisionID: String
    public let sequence: Int64
    public let kind: String
    public let text: String?
    public let deleted: Bool
    public let expiresAt: Date
    public init(id: String, revisionID: String, sequence: Int64, kind: String = "content", text: String?, deleted: Bool, expiresAt: Date) {
        self.id = id; self.revisionID = revisionID; self.sequence = sequence; self.kind = kind
        self.text = text; self.deleted = deleted; self.expiresAt = expiresAt
    }
    enum CodingKeys: String, CodingKey { case id = "paste_id", revisionID = "revision_id", sequence = "server_sequence", kind, text, deleted, expiresAt = "expires_at" }
}

public struct SnapshotPage: Decodable, Equatable {
    public let cursor: Int64
    public let pastes: [PasteRecord]
    enum CodingKeys: String, CodingKey { case cursor, pastes }
}

public enum SyncEventKind: String, Codable {
    case content
    case tombstone
    case imageBundle = "image_bundle"
}

public struct SyncEvent: Codable, Equatable {
    public let sequence: Int64
    public let pasteID: String
    public let revisionID: String
    public let kind: SyncEventKind
    public let text: String?
    public let deleted: Bool
    public init(sequence: Int64, pasteID: String, revisionID: String, kind: SyncEventKind, text: String?, deleted: Bool) {
        self.sequence = sequence; self.pasteID = pasteID; self.revisionID = revisionID
        self.kind = kind; self.text = text; self.deleted = deleted
    }
    enum CodingKeys: String, CodingKey { case sequence, pasteID = "paste_id", revisionID = "revision_id", kind, text, deleted }
}

public struct SyncPage: Codable, Equatable {
    public let cursor: Int64
    public let hasMore: Bool
    public let events: [SyncEvent]
    public init(cursor: Int64, hasMore: Bool, events: [SyncEvent]) {
        self.cursor = cursor; self.hasMore = hasMore; self.events = events
    }
    enum CodingKeys: String, CodingKey { case cursor, hasMore = "has_more", events }
}

public struct WorkspaceGrant: Decodable, Equatable {
    public let workspaceID: String
    public let deviceID: String
    public let credentials: [CredentialRecord]
    public let recoveryCode: String?
    public init(workspaceID: String, deviceID: String, credentials: [CredentialRecord] = [], recoveryCode: String? = nil) {
        self.workspaceID = workspaceID; self.deviceID = deviceID; self.credentials = credentials; self.recoveryCode = recoveryCode
    }
    enum CodingKeys: String, CodingKey { case workspaceID = "workspace_id", device, credentials, recoveryCode = "recovery_code" }
    enum DeviceKeys: String, CodingKey { case id }
    public init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        workspaceID = try values.decode(String.self, forKey: .workspaceID)
        let device = try values.nestedContainer(keyedBy: DeviceKeys.self, forKey: .device)
        deviceID = try device.decode(String.self, forKey: .id)
        credentials = try values.decode([CredentialRecord].self, forKey: .credentials)
        recoveryCode = try values.decodeIfPresent(String.self, forKey: .recoveryCode)
    }
}

public struct CredentialRecord: Codable, Equatable {
    public let kind: String
    public let token: String
    public init(kind: String, token: String) { self.kind = kind; self.token = token }
}

public struct DeviceRecord: Codable, Equatable, Identifiable {
    public let id: String
    public let displayName: String
    public let platform: String
    public let role: String
    public let isCurrent: Bool
    public init(id: String, displayName: String, platform: String, role: String, isCurrent: Bool) {
        self.id = id; self.displayName = displayName; self.platform = platform
        self.role = role; self.isCurrent = isCurrent
    }
    enum CodingKeys: String, CodingKey { case id, displayName = "display_name", platform, role, isCurrent = "is_current" }
}

public enum APIError: Error, Equatable {
    case unauthorized
    case forbidden
    case notFound
    case conflict
    case cursorExpired
    case invalidResponse
    case http(status: Int)
    case transport
}
