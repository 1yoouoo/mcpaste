import AppKit
import XCTest
@testable import MCPasteApp

final class BridgeIconViewTests: XCTestCase {
    func testMenuBarImageUsesSmallIntrinsicSize() {
        let image = BridgeIconView.menuBarImage()

        XCTAssertNotNil(image)
        XCTAssertEqual(image?.size, NSSize(width: 18, height: 18))
    }
}
