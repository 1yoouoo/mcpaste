import XCTest
@testable import MCPasteCore

final class PackageSmokeTests: XCTestCase {
    func testPackageLoads() {
        XCTAssertEqual(MCPasteCoreVersion.current, "0.2.3")
    }
}
