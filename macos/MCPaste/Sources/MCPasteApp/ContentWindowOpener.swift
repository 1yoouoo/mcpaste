import AppKit

/// A menu bar app is an accessory app: `openWindow` alone leaves the new window
/// behind whichever app is frontmost, and the popover stays pinned on screen.
/// Opening the content window therefore also activates the app and closes the popover.
enum ContentWindowOpener {
    static func open(openWindow: () -> Void, activate: () -> Void, dismissPopover: (() -> Void)? = nil) {
        openWindow()
        activate()
        dismissPopover?()
    }

    static func activateApp() {
        NSApp.activate(ignoringOtherApps: true)
    }
}
