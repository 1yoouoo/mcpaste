import SwiftUI

struct RecoveryView: View {
    @ObservedObject var model: AppModel
    @State private var confirmed = false

    var body: some View {
        VStack(spacing: 0) {
            PopoverHeader(title: "Recovery code") { EmptyView() }
            Divider()

            VStack(alignment: .leading, spacing: 12) {
                HStack(alignment: .firstTextBaseline, spacing: 7) {
                    Image(systemName: "key.fill")
                        .foregroundStyle(.orange)
                    Text("This code is shown once. Store it somewhere safe — it is the only way to restore this workspace.")
                        .font(.callout)
                        .fixedSize(horizontal: false, vertical: true)
                }

                RecoveryCodeField(model: model)

                Toggle("I saved my recovery code", isOn: $confirmed)
                    .toggleStyle(.checkbox)

                Button {
                    model.completeRecoverySetup()
                } label: {
                    Text("Continue").frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .disabled(!confirmed)
                .keyboardShortcut(.defaultAction)
            }
            .padding(12)
        }
        .frame(width: 340)
    }
}

private struct RecoveryCodeField: View {
    @ObservedObject var model: AppModel
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("Recovery code")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                Button(copied ? "Copied" : "Copy") {
                    model.copyRecoveryCode()
                    copied = true
                    Task { @MainActor in
                        try? await Task.sleep(nanoseconds: 1_500_000_000)
                        copied = false
                    }
                }
                .buttonStyle(.link)
                .font(.caption)
            }
            Text(model.recoveryCode)
                .font(.system(.body, design: .monospaced))
                .textSelection(.enabled)
                .padding(10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(.quaternary, in: RoundedRectangle(cornerRadius: 8))
            Text("Do not share this code.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }
}
