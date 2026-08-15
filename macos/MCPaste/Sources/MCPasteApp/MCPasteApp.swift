import SwiftUI

@main
struct MCPasteApp: App {
    @StateObject private var model = AppModel()
    var body: some Scene {
        MenuBarExtra {
            switch model.screen {
            case .onboarding: OnboardingView(model: model)
            case .pasteboard: PastePopoverView(model: model)
            }
        } label: {
            BridgeIconView()
                .accessibilityLabel("MCPaste")
        }
        .menuBarExtraStyle(.window)
    }
}
