import Foundation
import Network
import XCTest
@testable import MCPasteCore

final class APIClientTests: XCTestCase {
    func testPasteRecordDecodesExactTextAndOrderedAttachments() throws {
        let data = Data(
            #"{"paste_id":"p1","revision_id":"r2","attachment_revision_id":"a1","server_sequence":2,"kind":"content","text":"line 1\nline 2  ","deleted":false,"expires_at":"2027-08-14T00:00:00Z","assets":[{"asset_index":0,"mime_type":"image/png","width":40,"height":30,"byte_size":120,"expires_at":"2026-08-15T00:00:00Z"},{"asset_index":1,"mime_type":"image/jpeg","width":80,"height":60,"byte_size":240,"expires_at":"2026-08-16T00:00:00Z"}]}"#.utf8
        )
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601

        let paste = try decoder.decode(PasteRecord.self, from: data)

        XCTAssertEqual(paste.text, "line 1\nline 2  ")
        XCTAssertEqual(paste.attachmentRevisionID, "a1")
        XCTAssertEqual(paste.attachments.map(\.assetIndex), [0, 1])
        XCTAssertEqual(paste.attachments.map(\.mimeType), ["image/png", "image/jpeg"])
    }

    func testPasteRecordDefaultsMissingAssetsToEmpty() throws {
        let data = Data(
            #"{"paste_id":"p1","revision_id":"r1","server_sequence":1,"kind":"content","text":"text","deleted":false,"expires_at":"2027-08-14T00:00:00Z"}"#.utf8
        )
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601

        let paste = try decoder.decode(PasteRecord.self, from: data)

        XCTAssertNil(paste.attachmentRevisionID)
        XCTAssertEqual(paste.attachments, [])
    }

    func testSyncEventOmittedAssetsDoesNotTouchAttachments() throws {
        let data = Data(
            #"{"sequence":3,"paste_id":"p1","revision_id":"r3","kind":"content","text":"updated","deleted":false}"#.utf8
        )

        let event = try JSONDecoder().decode(SyncEvent.self, from: data)

        XCTAssertNil(event.attachments)
    }

    func testSyncEventEmptyAssetsExplicitlyClearsAttachments() throws {
        let data = Data(
            #"{"sequence":4,"paste_id":"p1","revision_id":"a2","kind":"attachment_bundle","text":null,"deleted":false,"assets":[]}"#.utf8
        )

        let event = try JSONDecoder().decode(SyncEvent.self, from: data)

        XCTAssertEqual(event.kind, .attachmentBundle)
        XCTAssertEqual(event.revisionID, "a2")
        XCTAssertNotNil(event.attachments)
        XCTAssertEqual(event.attachments, [])
    }

    func testSyncEventDecodesAttachmentAndLegacyImageBundleKinds() throws {
        let attachmentData = Data(
            #"{"sequence":4,"paste_id":"p1","revision_id":"a2","kind":"attachment_bundle","text":null,"deleted":false}"#.utf8
        )
        let legacyData = Data(
            #"{"sequence":5,"paste_id":"p1","revision_id":"i1","kind":"image_bundle","text":null,"deleted":false}"#.utf8
        )
        let decoder = JSONDecoder()

        let attachmentEvent = try decoder.decode(SyncEvent.self, from: attachmentData)
        let legacyEvent = try decoder.decode(SyncEvent.self, from: legacyData)

        XCTAssertEqual(attachmentEvent.kind, .attachmentBundle)
        XCTAssertEqual(attachmentEvent.revisionID, "a2")
        XCTAssertEqual(legacyEvent.kind, .imageBundle)
        XCTAssertEqual(legacyEvent.revisionID, "i1")
    }

    func testMCPasteAPIConformsToWorkspaceAPI() {
        let client: any WorkspaceAPI = MCPasteAPI(
            baseURL: URL(string: "https://example.test")!,
            token: "full-token"
        )

        XCTAssertNotNil(client)
    }

    func testReplaceAttachmentsSendsOrderedMultipartRequest() async throws {
        let client = makeStubbedClient { request in
            XCTAssertEqual(request.httpMethod, "PUT")
            XCTAssertEqual(request.url?.path, "/v1/pastes/p1/attachments")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer full-token")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Idempotency-Key"), "stable-key")
            XCTAssertTrue(request.value(forHTTPHeaderField: "Content-Type")?.hasPrefix("multipart/form-data; boundary=") == true)

            let body = try XCTUnwrap(requestBodyData(request))
            let bodyText = String(decoding: body, as: UTF8.self)
            let firstPart = try XCTUnwrap(bodyText.range(of: "FIRST-IMAGE"))
            let secondPart = try XCTUnwrap(bodyText.range(of: "SECOND-IMAGE"))
            XCTAssertLessThan(firstPart.lowerBound, secondPart.lowerBound)
            XCTAssertEqual(bodyText.components(separatedBy: "name=\"images\"").count - 1, 2)

            return StubbedResponse(
                statusCode: 200,
                body: Data(
                    #"{"paste_id":"p1","revision_id":"r1","attachment_revision_id":"a1","server_sequence":2,"kind":"content","text":"exact","deleted":false,"expires_at":"2027-08-14T00:00:00Z","assets":[]}"#.utf8
                )
            )
        }
        defer { StubURLProtocol.reset() }

        let record = try await client.replaceAttachments(
            pasteID: "p1",
            images: [
                NormalizedImage(mimeType: "image/png", width: 1, height: 2, data: Data("FIRST-IMAGE".utf8)),
                NormalizedImage(mimeType: "image/jpeg", width: 3, height: 4, data: Data("SECOND-IMAGE".utf8))
            ],
            idempotencyKey: "stable-key"
        )

        XCTAssertEqual(record.id, "p1")
        XCTAssertEqual(record.attachmentRevisionID, "a1")
    }

    func testReplaceAttachmentsAllowsEmptyMultipartToClear() async throws {
        let client = makeStubbedClient { request in
            XCTAssertEqual(request.httpMethod, "PUT")
            XCTAssertEqual(request.url?.path, "/v1/pastes/p1/attachments")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Idempotency-Key"), "clear-key")
            let bodyText = String(decoding: try XCTUnwrap(requestBodyData(request)), as: UTF8.self)
            XCTAssertFalse(bodyText.contains("name=\"images\""))
            XCTAssertTrue(bodyText.hasSuffix("--\r\n"))

            return StubbedResponse(
                statusCode: 200,
                body: Data(
                    #"{"paste_id":"p1","revision_id":"r1","attachment_revision_id":"a2","server_sequence":3,"kind":"content","text":"exact","deleted":false,"expires_at":"2027-08-14T00:00:00Z","assets":[]}"#.utf8
                )
            )
        }
        defer { StubURLProtocol.reset() }

        let record = try await client.replaceAttachments(
            pasteID: "p1",
            images: [],
            idempotencyKey: "clear-key"
        )

        XCTAssertEqual(record.attachments, [])
        XCTAssertEqual(record.attachmentRevisionID, "a2")
    }

    func testDownloadAttachmentUsesUnifiedAuthenticatedPath() async throws {
        let expected = Data([0, 1, 2, 3])
        let client = makeStubbedClient { request in
            XCTAssertEqual(request.httpMethod, "GET")
            XCTAssertEqual(request.url?.path, "/v1/pastes/p1/attachments/0")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer full-token")
            return StubbedResponse(statusCode: 200, body: expected)
        }
        defer { StubURLProtocol.reset() }

        let downloaded = try await client.downloadAttachment(pasteID: "p1", assetIndex: 0)

        XCTAssertEqual(downloaded, expected)
    }

    func testDownloadAttachmentRequiresSuccessfulResponse() async throws {
        let client = makeStubbedClient { _ in
            StubbedResponse(statusCode: 404, body: Data())
        }
        defer { StubURLProtocol.reset() }

        do {
            _ = try await client.downloadAttachment(pasteID: "p1", assetIndex: 0)
            XCTFail("Expected not-found error")
        } catch let error as APIError {
            XCTAssertEqual(error, .notFound)
        }
    }

    func testLookupPairingSendsAuthenticatedShortCodeRequest() async throws {
        let client = makeStubbedClient { request in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/v1/pairing-requests/lookup")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer full-token")
            XCTAssertTrue(requestBodyHasOnlyString(request, key: "short_code", expected: "12345678"))

            return StubbedResponse(
                statusCode: 200,
                body: Data(
                    #"{"pairing_id":"pair-1","proposed_name":"New Mac","platform":"macos","requested_scope":"full","status":"pending","expires_at":"2026-08-15T00:00:00Z"}"#.utf8
                )
            )
        }
        defer { StubURLProtocol.reset() }

        let details = try await client.lookupPairing(shortCode: "12345678")

        XCTAssertEqual(details.pairingID, "pair-1")
        XCTAssertEqual(details.proposedName, "New Mac")
        XCTAssertNil(details.claimExpiresAt)
    }

    func testApprovePairingSendsAuthenticatedEmptyJSONAndStableIdempotency() async throws {
        let client = makeStubbedClient { request in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/v1/pairing-requests/pair-1/approve")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer full-token")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Idempotency-Key"), "approve-key")
            XCTAssertTrue(requestBodyIsEmptyJSONObject(request))

            return StubbedResponse(
                statusCode: 200,
                body: Data(
                    #"{"pairing_id":"pair-1","status":"approved","claim_expires_at":"2026-08-15T00:05:00Z"}"#.utf8
                )
            )
        }
        defer { StubURLProtocol.reset() }

        let approval = try await client.approvePairing(id: "pair-1", idempotencyKey: "approve-key")

        XCTAssertEqual(approval.pairingID, "pair-1")
        XCTAssertEqual(approval.status, "approved")
    }

    func testPairingStatusSendsClaimProofOnlyInPublicJSONBody() async throws {
        let client = makeStubbedClient { request in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/v1/pairing-requests/pair-1/status")
            XCTAssertNil(request.url?.query)
            XCTAssertNil(request.value(forHTTPHeaderField: "Authorization"))

            let body = try XCTUnwrap(requestBodyData(request))
            let object = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: Any])
            XCTAssertEqual(Set(object.keys), ["claim_secret"])
            let proof = try XCTUnwrap(object["claim_secret"] as? String)
            XCTAssertEqual(proof.count, 43)
            XCTAssertTrue(proof.allSatisfy { $0 == "s" })
            XCTAssertFalse(request.url?.absoluteString.contains(proof) == true)
            XCTAssertFalse(request.allHTTPHeaderFields?.values.contains(proof) == true)

            return StubbedResponse(
                statusCode: 200,
                body: Data(
                    #"{"pairing_id":"pair-1","status":"pending","expires_at":"2026-08-15T00:00:00Z"}"#.utf8
                )
            )
        }
        defer { StubURLProtocol.reset() }

        let status = try await client.pairingStatus(
            id: "pair-1",
            claimSecret: String(repeating: "s", count: 43)
        )

        XCTAssertEqual(status.pairingID, "pair-1")
        XCTAssertEqual(status.status, "pending")
        XCTAssertNil(status.claimExpiresAt)
    }

    func testDenyPairingSendsAuthenticatedEmptyJSONAndStableIdempotency() async throws {
        let client = makeStubbedClient { request in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/v1/pairing-requests/pair-1/deny")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer full-token")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Idempotency-Key"), "deny-key")
            XCTAssertTrue(requestBodyIsEmptyJSONObject(request))

            return StubbedResponse(
                statusCode: 200,
                body: Data(
                    #"{"pairing_id":"pair-1","status":"denied","expires_at":"2026-08-15T00:00:00Z"}"#.utf8
                )
            )
        }
        defer { StubURLProtocol.reset() }

        let status = try await client.denyPairing(id: "pair-1", idempotencyKey: "deny-key")

        XCTAssertEqual(status.pairingID, "pair-1")
        XCTAssertEqual(status.status, "denied")
    }

    func testCreatePasteSendsBearerAndIdempotencyHeaders() async throws {
        let url = URL(string: "https://example.test")!
        let session = URLSession(configuration: .ephemeral)
        let client = MCPasteAPI(baseURL: url, token: "token", session: session)
        let request = try client.makeRequest(path: "/v1/pastes", method: "POST", body: CreatePasteRequest(text: "exact\ntext"), idempotencyKey: "idem")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer token")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Idempotency-Key"), "idem")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Content-Type"), "application/json")
    }

    func testHTTPStatusMappingDoesNotExposeResponseBody() async throws {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [UnauthorizedURLProtocol.self]
        let client = MCPasteAPI(baseURL: URL(string: "https://example.test")!, token: "token", session: URLSession(configuration: configuration))

        do {
            _ = try await client.createPaste(text: "secret")
            XCTFail("Expected unauthorized error")
        } catch let error as APIError {
            XCTAssertEqual(error, .unauthorized)
            XCTAssertFalse(String(describing: error).contains("secret"))
        }
    }

    func testEventStreamConsumesEventsAndSendsCursorHeaders() async throws {
        let server = try EventStreamHTTPServer(statusCode: 200, payload: "id: 18\ndata: first\n\nid: 21\ndata: second\n\n")
        let port = try await server.start()
        defer { server.stop() }
        let client = MCPasteAPI(
            baseURL: URL(string: "http://127.0.0.1:\(port)")!,
            token: "token",
            session: URLSession(configuration: .ephemeral)
        )

        var ids: [Int64] = []
        for try await id in client.eventStream(after: 17) {
            ids.append(id)
        }

        XCTAssertEqual(ids, [18, 21])
        XCTAssertTrue(server.request.contains("GET /v1/events?after=17 HTTP/1.1"))
        XCTAssertTrue(server.request.localizedCaseInsensitiveContains("last-event-id: 17"))
        XCTAssertTrue(server.request.localizedCaseInsensitiveContains("accept: text/event-stream"))
    }

    func testEventStreamMapsExpiredCursor() async throws {
        EventStreamURLProtocol.reset(statusCode: 410)
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [EventStreamURLProtocol.self]
        let client = MCPasteAPI(
            baseURL: URL(string: "http://example.test")!,
            token: "token",
            session: URLSession(configuration: configuration)
        )

        do {
            for try await _ in client.eventStream(after: 17) {}
            XCTFail("Expected cursor-expired error")
        } catch let error as APIError {
            XCTAssertEqual(error, .cursorExpired)
        }
    }

    func testEventStreamStopsWhenConsumerIsCancelled() async throws {
        let server = try EventStreamHTTPServer(statusCode: 200, payload: "", closeAfterResponse: false)
        let port = try await server.start()
        defer { server.stop() }
        let client = MCPasteAPI(
            baseURL: URL(string: "http://127.0.0.1:\(port)")!,
            token: "token",
            session: URLSession(configuration: .ephemeral)
        )

        let reader = Task { () -> [Int64] in
            var ids: [Int64] = []
            do {
                for try await id in client.eventStream(after: 17) {
                    ids.append(id)
                }
            } catch {}
            return ids
        }
        try await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertTrue(server.request.hasPrefix("GET /v1/events?after=17 HTTP/1.1"))
        reader.cancel()

        let ids = await reader.value
        XCTAssertEqual(ids, [])
    }

    private func makeStubbedClient(
        handler: @escaping (URLRequest) throws -> StubbedResponse
    ) -> MCPasteAPI {
        StubURLProtocol.handler = handler
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [StubURLProtocol.self]
        return MCPasteAPI(
            baseURL: URL(string: "https://example.test")!,
            token: "full-token",
            session: URLSession(configuration: configuration)
        )
    }
}

private struct StubbedResponse {
    let statusCode: Int
    let body: Data
}

private final class StubURLProtocol: URLProtocol {
    static var handler: ((URLRequest) throws -> StubbedResponse)?

    static func reset() {
        handler = nil
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        do {
            let response = try XCTUnwrap(Self.handler)(request)
            let http = HTTPURLResponse(
                url: request.url!,
                statusCode: response.statusCode,
                httpVersion: nil,
                headerFields: nil
            )!
            client?.urlProtocol(self, didReceive: http, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: response.body)
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}
}

private func requestBodyHasOnlyString(_ request: URLRequest, key: String, expected: String) -> Bool {
    guard
        let body = requestBodyData(request),
        let object = try? JSONSerialization.jsonObject(with: body) as? [String: Any],
        Set(object.keys) == [key],
        let value = object[key] as? String
    else {
        return false
    }
    return value == expected
}

private func requestBodyIsEmptyJSONObject(_ request: URLRequest) -> Bool {
    guard
        let body = requestBodyData(request),
        let object = try? JSONSerialization.jsonObject(with: body) as? [String: Any]
    else {
        return false
    }
    return object.isEmpty
}

private func requestBodyData(_ request: URLRequest) -> Data? {
    if let body = request.httpBody { return body }
    guard let stream = request.httpBodyStream else { return nil }

    stream.open()
    defer { stream.close() }

    var body = Data()
    var buffer = [UInt8](repeating: 0, count: 4_096)
    while true {
        let count = stream.read(&buffer, maxLength: buffer.count)
        if count < 0 { return nil }
        if count == 0 { return body }
        body.append(contentsOf: buffer[0..<count])
    }
}

private final class UnauthorizedURLProtocol: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let response = HTTPURLResponse(url: request.url!, statusCode: 401, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(#"{"error":"secret"}"#.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private final class EventStreamURLProtocol: URLProtocol {
    static var statusCode = 200
    static var lastRequest: URLRequest?

    static func reset(statusCode: Int) {
        self.statusCode = statusCode
        lastRequest = nil
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.lastRequest = request
        let payload = Data("id: 18\ndata: first\n\nid: 21\ndata: second\n\n".utf8)
        let response = HTTPURLResponse(
            url: request.url!,
            statusCode: Self.statusCode,
            httpVersion: nil,
            headerFields: [
                "Content-Type": "text/event-stream",
                "Content-Length": String(payload.count)
            ]
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        if Self.statusCode == 200 {
            DispatchQueue.global().asyncAfter(deadline: .now() + 0.01) { [weak self] in
                guard let self else { return }
                self.client?.urlProtocol(self, didLoad: payload)
                self.client?.urlProtocolDidFinishLoading(self)
            }
            return
        }
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

private final class EventStreamHTTPServer {
    private let listener: NWListener
    private let statusCode: Int
    private let payload: Data
    private let closeAfterResponse: Bool
    private(set) var request = ""

    init(statusCode: Int, payload: String, closeAfterResponse: Bool = true) throws {
        listener = try NWListener(using: .tcp, on: NWEndpoint.Port(rawValue: 0)!)
        self.statusCode = statusCode
        self.payload = Data(payload.utf8)
        self.closeAfterResponse = closeAfterResponse
    }

    func start() async throws -> UInt16 {
        try await withCheckedThrowingContinuation { continuation in
            listener.stateUpdateHandler = { [listener] state in
                switch state {
                case .ready:
                    continuation.resume(returning: listener.port!.rawValue)
                case let .failed(error):
                    continuation.resume(throwing: error)
                default:
                    break
                }
            }
            listener.newConnectionHandler = { [weak self] connection in
                self?.accept(connection)
            }
            listener.start(queue: DispatchQueue.global(qos: .userInitiated))
        }
    }

    func stop() {
        listener.cancel()
    }

    private func accept(_ connection: NWConnection) {
        connection.stateUpdateHandler = { state in
            if case .ready = state {
                connection.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { [weak self, connection] data, _, _, _ in
                    guard let self else { return }
                    self.request = String(decoding: data ?? Data(), as: UTF8.self)
                    let reason = self.statusCode == 200 ? "OK" : "Gone"
                    let headers: String
                    let body: Data
                    if self.closeAfterResponse {
                        headers = "HTTP/1.1 \(self.statusCode) \(reason)\r\nContent-Type: text/event-stream\r\nContent-Length: \(self.payload.count)\r\nConnection: close\r\n\r\n"
                        body = self.payload
                    } else {
                        headers = "HTTP/1.1 \(self.statusCode) \(reason)\r\nContent-Type: text/event-stream\r\nConnection: keep-alive\r\n\r\n"
                        body = Data("id: 18\n".utf8)
                    }
                    connection.send(content: Data(headers.utf8) + body, completion: .contentProcessed { _ in
                        if self.closeAfterResponse {
                            connection.cancel()
                        }
                    })
                }
            }
        }
        connection.start(queue: DispatchQueue.global(qos: .userInitiated))
    }
}
