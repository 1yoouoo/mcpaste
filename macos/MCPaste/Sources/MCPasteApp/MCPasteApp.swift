import SwiftUI
import AppKit

@main
struct MCPasteApp: App {
    @StateObject private var model = AppModel()
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        MenuBarExtra {
            StatusPopoverView(model: model)
        } label: {
            MenuBarLabel(model: model)
        }
        .menuBarExtraStyle(.window)

        Window("MCPaste", id: "content") {
            ContentWindowView(model: model)
                .onAppear { delegate.model = model }
        }
        .defaultSize(width: 760, height: 520)
    }
}

private struct MenuBarLabel: View {
    @ObservedObject var model: AppModel
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        BridgeIconView()
            .accessibilityLabel("MCPaste")
            .onAppear { openContentWindowIfReady() }
            .onChange(of: model.screen) { _, _ in openContentWindowIfReady() }
    }

    private func openContentWindowIfReady() {
        if model.screen == .pasteboard { openWindow(id: "content") }
    }
}

/// Quitting must not drop work, so termination waits for one last save.
final class AppDelegate: NSObject, NSApplicationDelegate {
    @MainActor var model: AppModel?

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        MainActor.assumeIsolated {
            guard let model, model.hasUnsavedWork else { return .terminateNow }
            Task {
                await model.saveIfNeeded()
                NSApplication.shared.reply(toApplicationShouldTerminate: true)
            }
            return .terminateLater
        }
    }
}
