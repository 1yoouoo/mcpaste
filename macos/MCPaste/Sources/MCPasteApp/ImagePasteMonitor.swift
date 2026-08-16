import AppKit

/// Watches ⌘V for the whole application.
///
/// The handler used to be installed per view, so a re-composed window could leave two
/// monitors behind and paste the same image twice. Installation is shared and reference
/// counted instead, which keeps exactly one monitor alive while any window wants it.
@MainActor
final class ImagePasteMonitor {
    static let shared = ImagePasteMonitor()

    private var monitor: Any?
    private var subscribers = 0
    private var handler: (([Data]) -> Void)?
    private var newPaste: (() -> Void)?

    private init() {}

    func subscribe(paste handler: @escaping ([Data]) -> Void, newPaste: @escaping () -> Void) {
        self.handler = handler
        self.newPaste = newPaste
        subscribers += 1
        guard monitor == nil else { return }
        monitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { [weak self] event in
            guard let self else { return event }
            // Matching by character breaks under a Hangul input source, so both shortcuts
            // are recognised by their physical key.
            if PasteShortcut.matchesNewPaste(event) {
                self.newPaste?()
                return nil
            }
            guard PasteShortcut.matches(event) else { return event }
            let images = ImagePasteboard.images(on: .general)
            guard !images.isEmpty else { return event }
            self.handler?(images)
            return nil
        }
    }

    func unsubscribe() {
        subscribers = max(0, subscribers - 1)
        guard subscribers == 0, let monitor else { return }
        NSEvent.removeMonitor(monitor)
        self.monitor = nil
        handler = nil
        newPaste = nil
    }
}
