import XCTest
@testable import MCPasteCore

final class EndpointConfigurationTests: XCTestCase {
    func testConfiguredEndpointIsTheBuildInjectedHTTPSOrigin() throws {
        let baseURL = try MCPasteEndpoint.baseURL()
        XCTAssertEqual(baseURL.scheme, "https")
        XCTAssertTrue(baseURL.path.isEmpty)
        XCTAssertTrue(MCPasteEndpoint.matchesConfiguredEndpoint(baseURL.absoluteString))
        XCTAssertFalse(MCPasteEndpoint.matchesConfiguredEndpoint(baseURL.absoluteString + "/"))
        XCTAssertFalse(MCPasteEndpoint.matchesConfiguredEndpoint("https://example.test/"))
    }

    func testConfiguredMCPURLAppendsOnlyTheMCPPath() throws {
        let mcpURL = try MCPasteEndpoint.mcpURL()
        XCTAssertEqual(mcpURL.path, "/v1/mcp")
    }
}
