import SwiftUI

struct OnboardingView: View {
    @ObservedObject var model: AppModel
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("MCPaste").font(.title2).bold()
            Text("Create or join a workspace to sync exact text across your devices.")
            TextField("Device name", text: $model.deviceName)
            if let error = model.errorMessage { Text(error).foregroundStyle(.red).font(.caption) }
            Button("Create workspace") { Task { await model.createWorkspace() } }.keyboardShortcut(.defaultAction)
            Divider()
            Text("Join an approved workspace").font(.headline)
            TextField("Pairing ID", text: $model.pairingID)
            SecureField("Claim secret", text: $model.claimSecret)
            Button("Join workspace") { Task { await model.joinWorkspace() } }
            Divider()
            Text("Recover a workspace").font(.headline)
            SecureField("Recovery code", text: $model.recoveryCode)
            Button("Recover workspace") { Task { await model.recoverWorkspace() } }
            if !model.recoveryCode.isEmpty { Text("Keep this recovery code safe; a successful recovery rotates it.").font(.caption).foregroundStyle(.secondary) }
        }.padding(20).frame(width: 320)
    }
}
