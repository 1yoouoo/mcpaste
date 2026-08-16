import SwiftUI
import AppKit

/// The paste editor.
///
/// SwiftUI's `TextEditor` hands drops and pastes to its own text view, which turns a dropped
/// screenshot into its file path and ignores images on the pasteboard entirely. This wraps
/// `NSTextView` so the window decides: images become attachments, text lands in the editor,
/// and everything else is refused rather than silently swallowed.
struct PasteTextEditor: NSViewRepresentable {
    @Binding var text: String
    /// The text view consumes drags itself, so it reports hovering back to the window;
    /// otherwise the drop hint appears everywhere except over the editor.
    @Binding var isDropTargeted: Bool
    let onImages: ([Data]) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeNSView(context: Context) -> NSScrollView {
        let textView = DropAwareTextView()
        textView.delegate = context.coordinator
        textView.onImages = onImages
        textView.onTargeted = { targeted in isDropTargeted = targeted }
        textView.isRichText = false
        textView.isAutomaticQuoteSubstitutionEnabled = false
        textView.isAutomaticDashSubstitutionEnabled = false
        textView.isAutomaticTextReplacementEnabled = false
        textView.isAutomaticSpellingCorrectionEnabled = false
        textView.allowsUndo = true
        textView.font = .monospacedSystemFont(ofSize: 12, weight: .regular)
        textView.textContainerInset = NSSize(width: 8, height: 10)
        textView.drawsBackground = false
        textView.string = text
        textView.registerForDraggedTypes(DroppedContent.acceptedTypes)

        // Without this the text view reports no width, which collapses the editor and
        // pushes the rest of the window out of place.
        textView.minSize = NSSize(width: 0, height: 0)
        textView.maxSize = NSSize(width: CGFloat.greatestFiniteMagnitude, height: CGFloat.greatestFiniteMagnitude)
        textView.isVerticallyResizable = true
        textView.isHorizontallyResizable = false
        textView.autoresizingMask = [.width]
        textView.textContainer?.widthTracksTextView = true
        textView.textContainer?.containerSize = NSSize(width: 0, height: CGFloat.greatestFiniteMagnitude)

        let scrollView = NSScrollView()
        scrollView.documentView = textView
        scrollView.hasVerticalScroller = true
        scrollView.hasHorizontalScroller = false
        scrollView.drawsBackground = false
        scrollView.autohidesScrollers = true
        scrollView.translatesAutoresizingMaskIntoConstraints = false
        return scrollView
    }

    func updateNSView(_ scrollView: NSScrollView, context: Context) {
        guard let textView = scrollView.documentView as? DropAwareTextView else { return }
        textView.onImages = onImages
        textView.onTargeted = { targeted in isDropTargeted = targeted }
        if textView.string != text {
            textView.string = text
        }
    }

    final class Coordinator: NSObject, NSTextViewDelegate {
        private let parent: PasteTextEditor

        init(_ parent: PasteTextEditor) { self.parent = parent }

        func textDidChange(_ notification: Notification) {
            guard let textView = notification.object as? NSTextView else { return }
            parent.text = textView.string
        }
    }
}

private final class DropAwareTextView: NSTextView {
    var onImages: (([Data]) -> Void)?
    var onTargeted: ((Bool) -> Void)?

    override func paste(_ sender: Any?) {
        switch DroppedContent.read(from: .general) {
        case .images(let data):
            onImages?(data)
        case .text, .none:
            super.pasteAsPlainText(sender)
        }
    }

    override func draggingEntered(_ sender: NSDraggingInfo) -> NSDragOperation {
        let accepted = acceptable(sender)
        onTargeted?(accepted)
        return accepted ? .copy : []
    }

    override func draggingUpdated(_ sender: NSDraggingInfo) -> NSDragOperation {
        acceptable(sender) ? .copy : []
    }

    override func draggingExited(_ sender: NSDraggingInfo?) {
        onTargeted?(false)
    }

    override func draggingEnded(_ sender: NSDraggingInfo) {
        onTargeted?(false)
    }

    override func performDragOperation(_ sender: NSDraggingInfo) -> Bool {
        onTargeted?(false)
        switch DroppedContent.read(from: sender.draggingPasteboard) {
        case .images(let data):
            onImages?(data)
            return true
        case .text(let text):
            insertText(text, replacementRange: selectedRange())
            return true
        case .none:
            return false
        }
    }

    private func acceptable(_ sender: NSDraggingInfo) -> Bool {
        DroppedContent.read(from: sender.draggingPasteboard) != .none
    }
}
