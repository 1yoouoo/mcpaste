import Foundation

public protocol SyncAPI: AnyObject {
    func sync(after: Int64) async throws -> SyncPage
}

public final class MCPasteAPI: SyncAPI {
    public let baseURL: URL
    public let token: String
    private let session: URLSession
    private let encoder = JSONEncoder()
    private let decoder = JSONDecoder()

    public init(baseURL: URL, token: String, session: URLSession = .shared) {
        self.baseURL = baseURL; self.token = token; self.session = session
        encoder.dateEncodingStrategy = .iso8601
        decoder.dateDecodingStrategy = .iso8601
    }

    public func makeRequest<T: Encodable>(path: String, method: String = "GET", body: T? = nil, idempotencyKey: String? = nil) throws -> URLRequest {
        guard let url = URL(string: path, relativeTo: baseURL) else { throw APIError.invalidResponse }
        var request = URLRequest(url: url)
        request.httpMethod = method
        if !token.isEmpty { request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let idempotencyKey { request.setValue(idempotencyKey, forHTTPHeaderField: "Idempotency-Key") }
        if let body {
            request.httpBody = try encoder.encode(body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return request
    }

    public func createWorkspace(deviceName: String) async throws -> WorkspaceGrant {
        try await send(path: "/v1/workspaces", method: "POST", body: ["device_name": deviceName, "platform": "macos"] as [String: String], idempotencyKey: UUID().uuidString)
    }

    public func createPairing(deviceName: String, scope: String = "full") async throws -> PairingRecord {
        try await send(path: "/v1/pairing-requests", method: "POST", body: ["proposed_name": deviceName, "platform": "macos", "requested_scope": scope] as [String: String], idempotencyKey: UUID().uuidString)
    }

    public func claimPairing(pairingID: String, claimSecret: String) async throws -> WorkspaceGrant {
        let request = try makeRequest(path: "/v1/pairing-requests/\(pairingID)/claim", method: "POST", body: ["claim_secret": claimSecret] as [String: String])
        let (data, _) = try await perform(request)
        do { return try decoder.decode(WorkspaceGrant.self, from: data) } catch { throw APIError.invalidResponse }
    }

    public func createPaste(text: String, idempotencyKey: String = UUID().uuidString) async throws -> PasteRecord {
        try await send(path: "/v1/pastes", method: "POST", body: CreatePasteRequest(text: text), idempotencyKey: idempotencyKey)
    }

    public func updatePaste(id: String, text: String, idempotencyKey: String = UUID().uuidString) async throws -> PasteRecord {
        try await send(path: "/v1/pastes/\(id)", method: "PATCH", body: CreatePasteRequest(text: text), idempotencyKey: idempotencyKey)
    }

    public func deletePaste(id: String, idempotencyKey: String = UUID().uuidString) async throws {
        _ = try await sendVoid(path: "/v1/pastes/\(id)", method: "DELETE", idempotencyKey: idempotencyKey)
    }

    public func listPastes() async throws -> [PasteRecord] { let response: PasteListResponse = try await send(path: "/v1/pastes"); return response.pastes }

    public func sync(after: Int64) async throws -> SyncPage { try await send(path: "/v1/sync?after=\(after)") }

    public func listDevices() async throws -> [DeviceRecord] { let response: DeviceListResponse = try await send(path: "/v1/devices"); return response.devices }

    public func renameDevice(id: String, displayName: String, idempotencyKey: String = UUID().uuidString) async throws -> DeviceRecord {
        try await send(path: "/v1/devices/\(id)", method: "PATCH", body: ["display_name": displayName] as [String: String], idempotencyKey: idempotencyKey)
    }

    public func revokeDevice(id: String, idempotencyKey: String = UUID().uuidString) async throws {
        _ = try await sendVoid(path: "/v1/devices/\(id)", method: "DELETE", idempotencyKey: idempotencyKey)
    }

    private func send<T: Decodable, Body: Encodable>(path: String, method: String = "GET", body: Body? = nil, idempotencyKey: String? = nil) async throws -> T {
        let request = try makeRequest(path: path, method: method, body: body, idempotencyKey: idempotencyKey)
        let (data, _) = try await perform(request)
        guard !data.isEmpty else { throw APIError.invalidResponse }
        do { return try decoder.decode(T.self, from: data) } catch { throw APIError.invalidResponse }
    }

    private func send<T: Decodable>(path: String) async throws -> T {
        try await send(path: path, method: "GET", body: Optional<String>.none, idempotencyKey: nil)
    }

    private func sendVoid(path: String, method: String, idempotencyKey: String) async throws {
        let request = try makeRequest(path: path, method: method, body: Optional<String>.none, idempotencyKey: idempotencyKey)
        _ = try await perform(request)
    }

    private func perform(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        do {
            let (data, response) = try await session.data(for: request)
            guard let http = response as? HTTPURLResponse else { throw APIError.invalidResponse }
            switch http.statusCode {
            case 200..<300: return (data, http)
            case 401: throw APIError.unauthorized
            case 403: throw APIError.forbidden
            case 404: throw APIError.notFound
            case 409: throw APIError.conflict
            default: throw APIError.http(status: http.statusCode)
            }
        } catch let error as APIError { throw error } catch { throw APIError.transport }
    }
}

private struct PasteListResponse: Decodable { let pastes: [PasteRecord] }
private struct DeviceListResponse: Decodable { let devices: [DeviceRecord] }
public struct PairingRecord: Decodable, Equatable {
    public let pairingID: String
    public let shortCode: String
    public let claimSecret: String
    public let expiresAt: Date
    enum CodingKeys: String, CodingKey { case pairingID = "pairing_id", shortCode = "short_code", claimSecret = "claim_secret", expiresAt = "expires_at" }
}
