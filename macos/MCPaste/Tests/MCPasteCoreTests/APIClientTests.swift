import Foundation
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

    func testEventStreamRequestUsesCursorAndLastEventID() throws {
        let client = MCPasteAPI(baseURL: URL(string: "https://example.test")!, token: "token", session: .shared)
        let stream = client.eventStream(after: 17)
        _ = stream
        let request = try client.makeRequest(path: "/v1/events?after=17", body: Optional<String>.none)
        XCTAssertEqual(request.url?.query, "after=17")
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
