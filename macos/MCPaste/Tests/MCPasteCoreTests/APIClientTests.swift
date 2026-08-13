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

    func testHTTPStatusMappingDoesNotExposeResponseBody() throws {
        let error = APIError.http(status: 401)
        XCTAssertEqual(error, .unauthorized)
        XCTAssertFalse(String(describing: APIError.http(status: 500)).contains("secret"))
    }
}
