import SwiftUI

@main
struct MCPasteApp: App {
    @StateObject private var model = AppModel()
    var body: some Scene {
        MenuBarExtra("MCPaste", systemImage: "doc.on.clipboard") {
            switch model.screen {
            case .onboarding: OnboardingView(model: model)
            case .pasteboard: PastePopoverView(model: model)
            }
        }
        .menuBarExtraStyle(.window)
    }
}
