import Foundation

public enum PeerSyncState: String, Codable, Equatable, Sendable {
    case upToDate = "up_to_date"
    case updating
    case waitingToSync = "waiting_to_sync"
    case sourceOffline = "source_offline"
}

public struct PeerDevice: Codable, Equatable, Identifiable, Sendable {
    public let id: String
    public let displayName: String
    public let reachable: Bool
    public let isLocal: Bool
    public let isSource: Bool
    public let lastSeenAt: Date

    public init(
        id: String,
        displayName: String,
        reachable: Bool,
        isLocal: Bool,
        isSource: Bool,
        lastSeenAt: Date
    ) {
        self.id = id
        self.displayName = displayName
        self.reachable = reachable
        self.isLocal = isLocal
        self.isSource = isSource
        self.lastSeenAt = lastSeenAt
    }

    enum CodingKeys: String, CodingKey {
        case id
        case displayName = "display_name"
        case reachable
        case isLocal = "is_local"
        case isSource = "is_source"
        case lastSeenAt = "last_seen_at"
    }
}

public struct RuntimeRevision: Codable, Equatable, Comparable, Sendable {
    public let wallMillis: Int64
    public let logical: UInt32
    public let deviceID: String

    public init(wallMillis: Int64, logical: UInt32, deviceID: String) {
        self.wallMillis = wallMillis
        self.logical = logical
        self.deviceID = deviceID
    }

    public static func < (lhs: RuntimeRevision, rhs: RuntimeRevision) -> Bool {
        if lhs.wallMillis != rhs.wallMillis { return lhs.wallMillis < rhs.wallMillis }
        if lhs.logical != rhs.logical { return lhs.logical < rhs.logical }
        return lhs.deviceID < rhs.deviceID
    }

    enum CodingKeys: String, CodingKey {
        case wallMillis = "wall_millis"
        case logical
        case deviceID = "device_id"
    }
}

public struct RuntimeAsset: Equatable, Sendable {
    public let digest: String
    public let mimeType: String
    public let width: Int
    public let height: Int
    public let data: Data

    public init(digest: String, mimeType: String, width: Int, height: Int, data: Data) {
        self.digest = digest
        self.mimeType = mimeType
        self.width = width
        self.height = height
        self.data = data
    }
}

public struct RuntimeContext: Equatable, Sendable {
    public let revision: RuntimeRevision
    public let sourceDeviceID: String
    public let updatedAt: Date
    public let text: String
    public let assets: [RuntimeAsset]
    public let sourceReachable: Bool
    public let syncState: PeerSyncState

    public init(
        revision: RuntimeRevision,
        sourceDeviceID: String,
        updatedAt: Date,
        text: String,
        assets: [RuntimeAsset],
        sourceReachable: Bool,
        syncState: PeerSyncState
    ) {
        self.revision = revision
        self.sourceDeviceID = sourceDeviceID
        self.updatedAt = updatedAt
        self.text = text
        self.assets = assets
        self.sourceReachable = sourceReachable
        self.syncState = syncState
    }
}

public struct PublicationResult: Codable, Equatable, Sendable {
    public let revision: RuntimeRevision
    public let syncState: PeerSyncState

    public init(revision: RuntimeRevision, syncState: PeerSyncState) {
        self.revision = revision
        self.syncState = syncState
    }

    enum CodingKeys: String, CodingKey {
        case revision
        case syncState = "sync_state"
    }
}

public enum PeerRuntimeError: Error, Equatable, Sendable {
    case empty
    case sourceOffline
    case unavailable
    case invalidResponse
    case rejectedImage
    case conflict
}

public protocol PeerRuntimeServing: Sendable {
    func publish(
        text: String,
        images: [NormalizedImage],
        expectedRevision: RuntimeRevision?
    ) async throws -> PublicationResult
    func current() async throws -> RuntimeContext?
    func devices() async throws -> [PeerDevice]
}
