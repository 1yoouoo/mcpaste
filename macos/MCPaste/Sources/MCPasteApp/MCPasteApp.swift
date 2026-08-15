import SwiftUI

@main
struct MCPasteApp: App {
    @StateObject private var model = AppModel()

    var body: some Scene {
        MenuBarExtra {
            StatusPopoverView(model: model)
        } label: {
            MenuBarLabel(model: model)
        }
        .menuBarExtraStyle(.window)

        Window("MCPaste", id: "content") {
            ContentWindowView(model: model)
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
