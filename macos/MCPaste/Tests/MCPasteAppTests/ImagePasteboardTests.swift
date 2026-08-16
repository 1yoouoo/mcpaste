import XCTest
import AppKit
@testable import MCPasteApp

final class ImagePasteboardTests: XCTestCase {
    private func makePasteboard() -> NSPasteboard {
        let pasteboard = NSPasteboard(name: NSPasteboard.Name("com.mcpaste.tests.\(UUID().uuidString)"))
        pasteboard.clearContents()
        return pasteboard
    }

    private func pngData() -> Data {
        let image = NSImage(size: NSSize(width: 4, height: 4))
        image.lockFocus()
        NSColor.systemTeal.drawSwatch(in: NSRect(x: 0, y: 0, width: 4, height: 4))
        image.unlockFocus()
        let tiff = image.tiffRepresentation!
        return NSBitmapImageRep(data: tiff)!.representation(using: .png, properties: [:])!
    }

    func testReadsImageDataFromThePasteboard() {
        let pasteboard = makePasteboard()
        pasteboard.setData(pngData(), forType: .png)

        let images = ImagePasteboard.images(on: pasteboard)

        XCTAssertEqual(images.count, 1)
        XCTAssertEqual(images.first?.prefix(4), Data([0x89, 0x50, 0x4E, 0x47]))
    }

    func testLeavesTextPastesToTheEditor() {
        let pasteboard = makePasteboard()
        pasteboard.setString("plain text", forType: .string)
        pasteboard.setData(pngData(), forType: .png)

        XCTAssertTrue(
            ImagePasteboard.images(on: pasteboard).isEmpty,
            "a pasteboard carrying text belongs to the editor, not the attachment strip"
        )
    }

    func testEmptyPasteboardYieldsNothing() {
        XCTAssertTrue(ImagePasteboard.images(on: makePasteboard()).isEmpty)
    }

    func testReadsEveryImageItemWhenSeveralAreCopied() {
        let pasteboard = makePasteboard()
        let first = NSPasteboardItem()
        first.setData(pngData(), forType: .png)
        let second = NSPasteboardItem()
        second.setData(pngData(), forType: .png)
        pasteboard.writeObjects([first, second])

        XCTAssertEqual(ImagePasteboard.images(on: pasteboard).count, 2)
    }

    func testTiffOnlyPasteboardIsAccepted() {
        let pasteboard = makePasteboard()
        let image = NSImage(size: NSSize(width: 4, height: 4))
        image.lockFocus()
        NSColor.systemPink.drawSwatch(in: NSRect(x: 0, y: 0, width: 4, height: 4))
        image.unlockFocus()
        pasteboard.setData(image.tiffRepresentation!, forType: .tiff)

        XCTAssertEqual(ImagePasteboard.images(on: pasteboard).count, 1)
    }
}

final class PasteShortcutTests: XCTestCase {
    func testMatchesCommandVOnTheLatinLayout() {
        XCTAssertTrue(PasteShortcut.matches(keyCode: 9, characters: "v", charactersIgnoringModifiers: "v", modifiers: [.command]))
    }

    // A Hangul input source reports the V key as "ㅍ", which is why matching on the
    // character alone left the shortcut dead for anyone typing Korean.
    func testMatchesCommandVWhileAKoreanInputSourceIsActive() {
        XCTAssertTrue(PasteShortcut.matches(keyCode: 9, characters: "v", charactersIgnoringModifiers: "ㅍ", modifiers: [.command]))
    }

    func testMatchesWhenOnlyTheKeyCodeSurvives() {
        XCTAssertTrue(PasteShortcut.matches(keyCode: 9, characters: nil, charactersIgnoringModifiers: nil, modifiers: [.command]))
    }

    func testIgnoresOtherKeys() {
        XCTAssertFalse(PasteShortcut.matches(keyCode: 8, characters: "c", charactersIgnoringModifiers: "c", modifiers: [.command]))
    }

    func testRequiresCommand() {
        XCTAssertFalse(PasteShortcut.matches(keyCode: 9, characters: "v", charactersIgnoringModifiers: "v", modifiers: []))
    }

    func testIgnoresOptionAndControlVariants() {
        XCTAssertFalse(PasteShortcut.matches(keyCode: 9, characters: "v", charactersIgnoringModifiers: "v", modifiers: [.command, .option]))
        XCTAssertFalse(PasteShortcut.matches(keyCode: 9, characters: "v", charactersIgnoringModifiers: "v", modifiers: [.command, .control]))
    }

    func testAllowsShiftForPasteAndMatchStyle() {
        XCTAssertTrue(PasteShortcut.matches(keyCode: 9, characters: "v", charactersIgnoringModifiers: "v", modifiers: [.command, .shift]))
    }
}

extension PasteShortcutTests {
    func testMatchesCommandNWhileAKoreanInputSourceIsActive() {
        XCTAssertTrue(PasteShortcut.matchesNewPaste(keyCodeEvent: 45, ignoring: "ㅜ"))
    }

    func testCommandNDoesNotMatchThePasteShortcut() {
        XCTAssertFalse(PasteShortcut.matches(keyCode: 45, characters: "n", charactersIgnoringModifiers: "ㅜ", modifiers: [.command]))
    }
}

private extension PasteShortcut {
    static func matchesNewPaste(keyCodeEvent: UInt16, ignoring: String) -> Bool {
        matches(keyCode: keyCodeEvent, characters: "n", charactersIgnoringModifiers: ignoring,
                modifiers: [.command], expecting: nKeyCode, letter: "n")
    }
}
