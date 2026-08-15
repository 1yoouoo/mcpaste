import XCTest
import MCPasteCore
@testable import MCPasteApp

@MainActor
final class AppModelTests: XCTestCase {
    func testEmptyModelStartsInOnboarding() {
        XCTAssertEqual(AppScreen.onboarding, AppModel().screen)
    }

    func testOnboardingModelUsesBuildConfiguredEndpointPolicy() throws {
        let configuredEndpoint = try MCPasteEndpoint.baseURL().absoluteString
        XCTAssertTrue(MCPasteEndpoint.matchesConfiguredEndpoint(configuredEndpoint))
        XCTAssertFalse(MCPasteEndpoint.matchesConfiguredEndpoint(configuredEndpoint + "/"))
    }
}
