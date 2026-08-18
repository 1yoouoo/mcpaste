import SwiftUI

private enum OnboardingRoute { case start, join, recover }

struct OnboardingView: View {
    @ObservedObject var model: AppModel
    @State private var route: OnboardingRoute = .start

    var body: some View {
        VStack(spacing: 0) {
            switch route {
            case .start:
                StartPane(model: model) { route = $0 }
            case .join:
                JoinPane(model: model) { route = .start }
            case .recover:
                RecoverPane(model: model) { route = .start }
            }
        }
        .frame(width: 320)
    }
}

private struct StartPane: View {
    @ObservedObject var model: AppModel
    let go: (OnboardingRoute) -> Void

    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 12) {
                Text("Sync exact text and images to your AI tools.")
                    .font(.callout)
                    .fixedSize(horizontal: false, vertical: true)

                LabeledField(label: "Device name") {
                    TextField("Device name", text: $model.deviceName)
                        .textFieldStyle(.roundedBorder)
                        .labelsHidden()
                }

                if let error = model.errorMessage {
                    Text(error)
                        .font(.caption)
                        .foregroundStyle(.red)
                        .fixedSize(horizontal: false, vertical: true)
                }

                Button {
                    Task { await model.createWorkspace() }
                } label: {
                    Text("Create workspace").frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .disabled(model.pending || model.deviceName.isEmpty)

                Text("A recovery code is shown once after setup. Save it to restore this workspace later.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(12)

            Divider()

            VStack(spacing: 1) {
                MenuRow(title: "Join with a pairing code…", chevron: true) { go(.join) }
                MenuRow(title: "Recover with a recovery code…", chevron: true) { go(.recover) }
            }
            .padding(6)
        }
    }
}

private struct JoinPane: View {
    @ObservedObject var model: AppModel
    let back: () -> Void

    var body: some View {
        SubPane(title: "Join a workspace", back: back) {
            Text("Ask an administrator to approve this Mac, then enter the pairing details they give you.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            LabeledField(label: "Pairing ID") {
                TextField("Pairing ID", text: $model.pairingID)
                    .textFieldStyle(.roundedBorder)
                    .labelsHidden()
            }
            LabeledField(label: "Claim secret") {
                SecureField("Claim secret", text: $model.claimSecret)
                    .textFieldStyle(.roundedBorder)
                    .labelsHidden()
            }
            if let error = model.errorMessage {
                Text(error).font(.caption).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
            Button {
                Task { await model.joinWorkspace() }
            } label: {
                Text("Join workspace").frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .disabled(model.pending || model.pairingID.isEmpty || model.claimSecret.isEmpty)
        }
    }
}

private struct RecoverPane: View {
    @ObservedObject var model: AppModel
    let back: () -> Void

    var body: some View {
        SubPane(title: "Recover a workspace", back: back) {
            Text("Enter the recovery code you saved. Recovering replaces it with a new code.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            LabeledField(label: "Recovery code") {
                SecureField("Recovery code", text: $model.recoveryCode)
                    .textFieldStyle(.roundedBorder)
                    .labelsHidden()
            }
            if let error = model.errorMessage {
                Text(error).font(.caption).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
            Button {
                Task { await model.recoverWorkspace() }
            } label: {
                Text("Recover workspace").frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .disabled(model.pending || model.recoveryCode.isEmpty)
        }
    }
}

private struct SubPane<Content: View>: View {
    let title: String
    let back: () -> Void
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Button(action: back) {
                HStack(spacing: 6) {
                    Image(systemName: "chevron.left").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                    Text(title).font(.system(size: 13, weight: .semibold))
                    Spacer()
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            content
        }
        .padding(12)
    }
}

struct LabeledField<Content: View>: View {
    let label: String
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label).font(.caption).foregroundStyle(.secondary)
            content
        }
    }
}
