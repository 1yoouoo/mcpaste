import XCTest
@testable import MCPasteApp

@MainActor
final class AppModelTests: XCTestCase {
    func testEmptyModelStartsInOnboarding() {
        XCTAssertEqual(AppScreen.onboarding, AppModel().screen)
    }
}
