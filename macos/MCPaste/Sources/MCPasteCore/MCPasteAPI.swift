import Foundation

public protocol SyncAPI: AnyObject {
    func sync(after: Int64) async throws -> SyncPage
}

public protocol EventsAPI: AnyObject {
    func eventStream(after: Int64) -> AsyncThrowingStream<Int64, Error>
}

public protocol SnapshotAPI: AnyObject {
    func snapshot() async throws -> SnapshotPage
}

public final class MCPasteAPI: SyncAPI, SnapshotAPI, EventsAPI {
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
        try await send(path: "/v1/workspaces", method: "POST", body: ["device_name": deviceName, "platform": "macos"] as [String: String], idempotencyKey: UUID().uuidString.lowercased())
    }

    public func createPairing(deviceName: String, scope: String = "full") async throws -> PairingRecord {
        try await send(path: "/v1/pairing-requests", method: "POST", body: ["proposed_name": deviceName, "platform": "macos", "requested_scope": scope] as [String: String], idempotencyKey: UUID().uuidString.lowercased())
    }

    public func claimPairing(pairingID: String, claimSecret: String) async throws -> WorkspaceGrant {
        let request = try makeRequest(path: "/v1/pairing-requests/\(pairingID)/claim", method: "POST", body: ["claim_secret": claimSecret] as [String: String])
        let (data, _) = try await perform(request)
        do { return try decoder.decode(WorkspaceGrant.self, from: data) } catch { throw APIError.invalidResponse }
    }

    public func recoverWorkspace(recoveryCode: String, deviceName: String) async throws -> WorkspaceGrant {
        try await send(path: "/v1/recoveries", method: "POST", body: [
            "recovery_code": recoveryCode,
            "device_name": deviceName,
            "platform": "macos"
        ] as [String: String], idempotencyKey: UUID().uuidString.lowercased())
    }

    public func createPaste(text: String, idempotencyKey: String = UUID().uuidString.lowercased()) async throws -> PasteRecord {
        try await send(path: "/v1/pastes", method: "POST", body: CreatePasteRequest(text: text), idempotencyKey: idempotencyKey)
    }

    public func updatePaste(id: String, text: String, idempotencyKey: String = UUID().uuidString.lowercased()) async throws -> PasteRecord {
        try await send(path: "/v1/pastes/\(id)", method: "PATCH", body: CreatePasteRequest(text: text), idempotencyKey: idempotencyKey)
    }

    public func deletePaste(id: String, idempotencyKey: String = UUID().uuidString.lowercased()) async throws {
        _ = try await sendVoid(path: "/v1/pastes/\(id)", method: "DELETE", idempotencyKey: idempotencyKey)
    }

    public func uploadImages(_ images: [NormalizedImage], idempotencyKey: String = UUID().uuidString.lowercased()) async throws -> PasteRecord {
        guard !images.isEmpty else { throw APIError.invalidResponse }
        let boundary = "MCPaste-\(UUID().uuidString)"
        var body = Data()
        for (index, image) in images.enumerated() {
            body.append(Data("--\(boundary)\r\n".utf8))
            body.append(Data("Content-Disposition: form-data; name=\"images\"; filename=\"asset-\(index).png\"\r\n".utf8))
            body.append(Data("Content-Type: \(image.mimeType)\r\n".utf8))
            body.append(Data("X-MCPaste-Width: \(image.width)\r\nX-MCPaste-Height: \(image.height)\r\n\r\n".utf8))
            body.append(image.data)
            body.append(Data("\r\n".utf8))
        }
        body.append(Data("--\(boundary)--\r\n".utf8))
        guard let url = URL(string: "/v1/image-pastes", relativeTo: baseURL) else { throw APIError.invalidResponse }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.httpBody = body
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        request.setValue(idempotencyKey, forHTTPHeaderField: "Idempotency-Key")
        let (data, _) = try await perform(request)
        do { return try decoder.decode(PasteRecord.self, from: data) } catch { throw APIError.invalidResponse }
    }

    public func listPastes() async throws -> [PasteRecord] { let response: PasteListResponse = try await send(path: "/v1/pastes"); return response.pastes }

    public func sync(after: Int64) async throws -> SyncPage { try await send(path: "/v1/sync?after=\(after)") }

    public func eventStream(after: Int64) -> AsyncThrowingStream<Int64, Error> {
        AsyncThrowingStream { continuation in
            let cancellation = EventStreamCancellation()
            cancellation.task = Task {
                do {
					var request = try makeRequest(path: "/v1/events?after=\(after)", body: Optional<String>.none)
                    request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
                    request.setValue(String(after), forHTTPHeaderField: "Last-Event-ID")
                    let (bytes, response) = try await session.bytes(for: request)
                    guard let http = response as? HTTPURLResponse else { throw APIError.invalidResponse }
                    guard (200..<300).contains(http.statusCode) else {
                        if http.statusCode == 401 { throw APIError.unauthorized }
                        if http.statusCode == 403 { throw APIError.forbidden }
                        if http.statusCode == 410 { throw APIError.cursorExpired }
                        throw APIError.http(status: http.statusCode)
                    }
                    var eventID: Int64?
                    for try await line in bytes.lines {
                        if line.hasPrefix("id:") {
                            eventID = Int64(line.dropFirst(3).trimmingCharacters(in: .whitespaces))
						} else if line.isEmpty, let value = eventID {
							continuation.yield(value)
							eventID = nil
                        }
                    }
                    continuation.finish()
                } catch is CancellationError {
                    continuation.finish()
                } catch let error as APIError {
                    continuation.finish(throwing: error)
                } catch {
                    continuation.finish(throwing: APIError.transport)
                }
            }
            continuation.onTermination = { _ in cancellation.task?.cancel() }
        }
    }

    public func snapshot() async throws -> SnapshotPage { try await send(path: "/v1/snapshot") }

    public func listDevices() async throws -> [DeviceRecord] { let response: DeviceListResponse = try await send(path: "/v1/devices"); return response.devices }

    public func renameDevice(id: String, displayName: String, idempotencyKey: String = UUID().uuidString.lowercased()) async throws -> DeviceRecord {
        try await send(path: "/v1/devices/\(id)", method: "PATCH", body: ["display_name": displayName] as [String: String], idempotencyKey: idempotencyKey)
    }

    public func revokeDevice(id: String, idempotencyKey: String = UUID().uuidString.lowercased()) async throws {
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
            case 410: throw APIError.cursorExpired
            default: throw APIError.http(status: http.statusCode)
            }
        } catch let error as APIError { throw error } catch { throw APIError.transport }
    }
}

private final class EventStreamCancellation: @unchecked Sendable {
    var task: Task<Void, Never>?
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
