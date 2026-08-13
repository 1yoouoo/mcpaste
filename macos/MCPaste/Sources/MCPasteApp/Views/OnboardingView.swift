import SwiftUI

struct OnboardingView: View {
    @ObservedObject var model: AppModel
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("MCPaste").font(.title2).bold()
            Text("Create or join a workspace to sync exact text across your devices.")
            TextField("https://mcpaste.example", text: $model.endpoint).textContentType(.URL)
            TextField("Device name", text: $model.deviceName)
            if let error = model.errorMessage { Text(error).foregroundStyle(.red).font(.caption) }
            Button("Create workspace") { Task { await model.createWorkspace() } }.keyboardShortcut(.defaultAction)
        }.padding(20).frame(width: 320)
    }
}
