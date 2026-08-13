import Foundation
import Network
import XCTest
@testable import MCPasteCore

final class APIClientTests: XCTestCase {
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
