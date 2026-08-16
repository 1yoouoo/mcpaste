import AppKit
import UniformTypeIdentifiers

/// What a drop carries, once the pasteboard has been inspected.
///
/// Dropping an image file on a text view would otherwise insert its path, which is how a
/// screenshot ended up in a paste as `/Users/…/Screenshot.png`. Images win over any text the
/// pasteboard also carries, because the file name is never what the drop meant.
enum DroppedContent: Equatable {
    case images([Data])
    case text(String)
    case none

    static let acceptedTypes: [NSPasteboard.PasteboardType] = [.png, .tiff, .fileURL, .string]

    static func read(from pasteboard: NSPasteboard) -> DroppedContent {
        let images = imageData(on: pasteboard)
        if !images.isEmpty { return .images(images) }
        if let text = text(on: pasteboard), !text.isEmpty { return .text(text) }
        return .none
    }

    private static func imageData(on pasteboard: NSPasteboard) -> [Data] {
        var found: [Data] = []
        for item in pasteboard.pasteboardItems ?? [] {
            if let data = [NSPasteboard.PasteboardType.png, .tiff].lazy.compactMap({ item.data(forType: $0) }).first {
                found.append(data)
                continue
            }
            if let url = fileURL(from: item), isImage(url), let data = try? Data(contentsOf: url) {
                found.append(data)
            }
        }
        return found
    }

    private static func text(on pasteboard: NSPasteboard) -> String? {
        if let string = pasteboard.string(forType: .string) { return string }
        for item in pasteboard.pasteboardItems ?? [] {
            guard let url = fileURL(from: item), !isImage(url) else { continue }
            if let contents = try? String(contentsOf: url, encoding: .utf8) { return contents }
        }
        return nil
    }

    private static func fileURL(from item: NSPasteboardItem) -> URL? {
        guard let value = item.string(forType: .fileURL) else { return nil }
        return URL(string: value)
    }

    private static func isImage(_ url: URL) -> Bool {
        guard let type = UTType(filenameExtension: url.pathExtension) else { return false }
        return type.conforms(to: .image)
    }
}
