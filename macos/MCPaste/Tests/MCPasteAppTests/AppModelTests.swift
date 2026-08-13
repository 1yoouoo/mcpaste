import XCTest
@testable import MCPasteApp

final class AppModelTests: XCTestCase {
    func testEmptyModelStartsInOnboarding() {
        XCTAssertEqual(AppScreen.onboarding, AppModel().screen)
    }
}
