import Foundation
import XCTest
@testable import MCPasteCore

final class ImageNormalizerTests: XCTestCase {
    func testPNGNormalizesToMetadataFreePNGAndPreservesDimensions() throws {
        let fixture = Data(base64Encoded: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")!
        let normalized = try ImageNormalizer().normalize(fixture)
        XCTAssertEqual(normalized.mimeType, "image/png")
        XCTAssertEqual(normalized.width, 1)
        XCTAssertEqual(normalized.height, 1)
        XCTAssertFalse(normalized.data.isEmpty)
    }

    func testOversizedSourceIsRejectedBeforeDecode() {
        XCTAssertThrowsError(try ImageNormalizer().normalize(Data(repeating: 1, count: ImageNormalizer.maxSourceBytes + 1)))
    }
}
