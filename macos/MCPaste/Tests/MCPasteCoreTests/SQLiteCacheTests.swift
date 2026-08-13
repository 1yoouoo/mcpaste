import XCTest
@testable import MCPasteCore

final class SQLiteCacheTests: XCTestCase {
    func testExactTextCursorAndTombstone() throws {
        let cache = try SQLiteCache(path: ":memory:")
        try cache.savePaste(CachedPaste(id: "p1", revisionID: "r1", sequence: 2, text: "line 1\r\n\nline 3", deleted: false, expiresAt: Date().addingTimeInterval(60)))
        XCTAssertEqual(try cache.paste(id: "p1")?.text, "line 1\r\n\nline 3")
        try cache.setCursor(2)
        XCTAssertEqual(try cache.cursor(), 2)
        try cache.savePaste(CachedPaste(id: "p1", revisionID: "r2", sequence: 3, text: nil, deleted: true, expiresAt: Date().addingTimeInterval(60)))
        XCTAssertNil(try cache.paste(id: "p1")?.text)
        XCTAssertTrue(try cache.paste(id: "p1")?.deleted == true)
    }
}
