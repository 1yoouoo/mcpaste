import XCTest
import AppKit
import UniformTypeIdentifiers
@testable import MCPasteApp

final class DroppedContentTests: XCTestCase {
    private func pasteboard() -> NSPasteboard {
        let board = NSPasteboard(name: NSPasteboard.Name("com.mcpaste.tests.drop.\(UUID().uuidString)"))
        board.clearContents()
        return board
    }

    private func pngData() -> Data {
        let image = NSImage(size: NSSize(width: 3, height: 3))
        image.lockFocus()
        NSColor.systemGreen.drawSwatch(in: NSRect(x: 0, y: 0, width: 3, height: 3))
        image.unlockFocus()
        return NSBitmapImageRep(data: image.tiffRepresentation!)!.representation(using: .png, properties: [:])!
    }

    func testImageDataDropIsAnAttachment() {
        let board = pasteboard()
        board.setData(pngData(), forType: .png)

        guard case .images(let images) = DroppedContent.read(from: board) else {
            return XCTFail("expected images")
        }
        XCTAssertEqual(images.count, 1)
    }

    func testImageFileDropIsAnAttachmentRatherThanItsPath() throws {
        let directory = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("shot.png")
        try pngData().write(to: file)

        let board = pasteboard()
        board.writeObjects([file as NSURL])

        guard case .images(let images) = DroppedContent.read(from: board) else {
            return XCTFail("an image file must attach, not insert its path")
        }
        XCTAssertEqual(images.first?.prefix(4), Data([0x89, 0x50, 0x4E, 0x47]))
    }

    func testTextDropGoesToTheEditor() {
        let board = pasteboard()
        board.setString("dropped text  \nsecond line", forType: .string)

        guard case .text(let text) = DroppedContent.read(from: board) else {
            return XCTFail("expected text")
        }
        XCTAssertEqual(text, "dropped text  \nsecond line", "exact spacing and line breaks must survive")
    }

    func testTextFileDropIsReadIntoTheEditor() throws {
        let directory = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("notes.txt")
        try "line one\nline two   ".write(to: file, atomically: true, encoding: .utf8)

        let board = pasteboard()
        board.writeObjects([file as NSURL])

        guard case .text(let text) = DroppedContent.read(from: board) else {
            return XCTFail("expected text from a text file")
        }
        XCTAssertEqual(text, "line one\nline two   ")
    }

    func testUnknownContentIsRejected() {
        let board = pasteboard()
        board.setData(Data([0x00, 0x01]), forType: NSPasteboard.PasteboardType("com.example.unknown"))

        guard case .none = DroppedContent.read(from: board) else {
            return XCTFail("unsupported content must be refused so the drop is not silently swallowed")
        }
    }

    func testImagesWinWhenAPasteboardCarriesBoth() {
        let board = pasteboard()
        board.setString("caption", forType: .string)
        board.setData(pngData(), forType: .png)

        guard case .images = DroppedContent.read(from: board) else {
            return XCTFail("a dropped image with an incidental caption is still an image")
        }
    }
}
