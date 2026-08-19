import AppKit
import XCTest
@testable import MCPasteApp

final class ConnectorIconViewTests: XCTestCase {
    func testKnownConnectorNamesMapToBundledSVGAssets() {
        XCTAssertEqual(ConnectorIconView.resourceName(for: "Codex"), "openai")
        XCTAssertEqual(ConnectorIconView.resourceName(for: "Claude Code"), "claude")
    }

    func testKnownConnectorIconsLoadAtPopoverSize() {
        XCTAssertEqual(ConnectorIconView.icon(for: "Codex")?.size, NSSize(width: 18, height: 18))
        XCTAssertEqual(ConnectorIconView.icon(for: "Claude Code")?.size, NSSize(width: 18, height: 18))
    }

    func testUnknownConnectorHasNoCustomAsset() {
        XCTAssertNil(ConnectorIconView.resourceName(for: "Other client"))
        XCTAssertNil(ConnectorIconView.icon(for: "Other client"))
    }
}
