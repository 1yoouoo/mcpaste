import AppKit

/// Reads images out of a pasteboard for ⌘V.
///
/// SwiftUI's `onPasteCommand` never fires for the content window, so the window installs a
/// key handler instead and asks this type what the pasteboard holds. A pasteboard carrying
/// text belongs to the editor, so only image-only pasteboards produce attachments.
/// Recognises ⌘V from a key event.
///
/// Matching on the typed character alone fails while a Hangul input source is active: the
/// V key then reports "ㅍ" as its unmodified character. The physical key code is the same
/// under every input source, so it decides, with the characters kept as a fallback for
/// layouts that place V elsewhere.
enum PasteShortcut {
    static let vKeyCode: UInt16 = 9
    static let nKeyCode: UInt16 = 45

    static func matches(
        keyCode: UInt16,
        characters: String?,
        charactersIgnoringModifiers: String?,
        modifiers: NSEvent.ModifierFlags,
        expecting key: UInt16 = vKeyCode,
        letter: String = "v"
    ) -> Bool {
        guard modifiers.contains(.command) else { return false }
        guard !modifiers.contains(.option), !modifiers.contains(.control) else { return false }
        if keyCode == key { return true }
        return [characters, charactersIgnoringModifiers]
            .compactMap { $0?.lowercased() }
            .contains(letter)
    }

    static func matches(_ event: NSEvent) -> Bool {
        matches(
            keyCode: event.keyCode,
            characters: event.characters,
            charactersIgnoringModifiers: event.charactersIgnoringModifiers,
            modifiers: event.modifierFlags
        )
    }

    static func matchesNewPaste(_ event: NSEvent) -> Bool {
        matches(
            keyCode: event.keyCode,
            characters: event.characters,
            charactersIgnoringModifiers: event.charactersIgnoringModifiers,
            modifiers: event.modifierFlags,
            expecting: nKeyCode,
            letter: "n"
        )
    }
}

enum ImagePasteboard {
    static let readableTypes: [NSPasteboard.PasteboardType] = [.png, .tiff]

    static func images(on pasteboard: NSPasteboard) -> [Data] {
        guard pasteboard.string(forType: .string) == nil else { return [] }
        guard let items = pasteboard.pasteboardItems else { return [] }
        return items.compactMap { item in
            readableTypes.lazy.compactMap { item.data(forType: $0) }.first
        }
    }
}
