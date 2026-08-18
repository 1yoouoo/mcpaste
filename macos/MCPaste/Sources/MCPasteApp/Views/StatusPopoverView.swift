import SwiftUI
import MCPasteCore

struct StatusPopoverView: View {
    @ObservedObject var model: AppModel

    var body: some View {
        switch model.screen {
        case .onboarding:
            OnboardingView(model: model)
        case .recovery:
            RecoveryView(model: model)
        case .pasteboard:
            WorkspaceStatusView(model: model)
        }
    }
}

private enum StatusRoute { case status, workspace }

private struct WorkspaceStatusView: View {
    @ObservedObject var model: AppModel
    @State private var route: StatusRoute = .status

    var body: some View {
        Group {
            switch route {
            case .status:
                StatusHome(model: model) { route = .workspace }
            case .workspace:
                WorkspaceDetail(model: model) { route = .status }
            }
        }
        .frame(width: 300)
    }
}

private struct StatusHome: View {
    @ObservedObject var model: AppModel
    @Environment(\.openWindow) private var openWindow
    @Environment(\.dismiss) private var dismiss
    let showWorkspace: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            VStack(spacing: 7) {
                HStack(spacing: 8) {
                    Text("Connection").font(.callout).foregroundStyle(.secondary)
                    Spacer(minLength: 8)
                    StatusPill(text: connection.title, color: connection.color)
                }
                // Recomputed on a schedule so "20 seconds ago" does not freeze while the popover stays open.
                TimelineView(.periodic(from: .now, by: 10)) { context in
                    InfoRow(label: "Last synced", value: lastSyncedLabel(for: model.lastSyncedAt, now: context.date))
                }
                if model.pendingCount > 0 {
                    InfoRow(label: "Queued", value: queuedText, tint: .orange) {
                        Button("Retry") { Task { await model.retryNow() } }
                            .buttonStyle(.link)
                    }
                }
                InfoRow(label: "Devices", value: devicesText)
                InfoRow(label: "MCP", value: mcpText)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)

            if let message = noticeMessage {
                Divider()
                HStack(alignment: .firstTextBaseline, spacing: 7) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .font(.caption)
                        .foregroundStyle(.orange)
                    Text(message)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                    Spacer(minLength: 0)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 9)
            }

            Divider()

            LatestPasteSummary(
                paste: model.history.first,
                number: model.history.first.map { model.displayNumber(for: $0) }
            )

            Divider()

            VStack(spacing: 1) {
                MenuRow(title: "Open Paste Window", prominent: true) {
                    ContentWindowOpener.open(
                        openWindow: { openWindow(id: "content") },
                        activate: ContentWindowOpener.activateApp,
                        dismissPopover: { dismiss() }
                    )
                }
                MenuRow(title: "Workspace & devices", chevron: true, action: showWorkspace)
                MenuRow(title: "Quit MCPaste") {
                    NSApplication.shared.terminate(nil)
                }
            }
            .padding(6)
        }
    }

    private var connection: (title: String, color: Color) {
        if model.syncFailed { return ("Offline", .red) }
        if model.pendingCount > 0 || model.pending { return ("Syncing", .orange) }
        switch model.syncStatus {
        case .realtime: return ("Realtime", .green)
        case .polling: return ("Polling", .green)
        case .notConnected: return ("Connecting", .secondary)
        }
    }

    /// A failed action explains itself; a dropped connection explains the retry.
    private var noticeMessage: String? {
        if let errorMessage = model.errorMessage { return errorMessage }
        if model.syncFailed { return "Sync is unavailable. The app keeps retrying." }
        return nil
    }

    private var queuedText: String {
        model.pendingCount == 1 ? "1 change" : "\(model.pendingCount) changes"
    }

    private var devicesText: String {
        AppModel.deviceCountLabel(for: model.devices)
    }

    private var mcpText: String {
        switch AppModel.mcpConnectorCount(in: model.devices) {
        case 0: return "No connectors"
        case 1: return "1 connector"
        case let count: return "\(count) connectors"
        }
    }
}

func lastSyncedLabel(for date: Date?, now: Date) -> String {
    guard let date else { return "Not yet" }
    return RelativeTime.string(for: date, now: now)
}

private struct LatestPasteSummary: View {
    let paste: CachedPaste?
    let number: Int?

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            Text("LATEST PASTE")
                .font(.system(size: 10, weight: .semibold))
                .tracking(0.6)
                .foregroundStyle(.tertiary)
            if let paste, !paste.deleted {
                Text(preview(paste))
                    .font(.system(size: 11, design: .monospaced))
                    .lineLimit(2)
                HStack(spacing: 5) {
                    if !paste.attachments.isEmpty {
                        Image(systemName: "photo").font(.caption2)
                        Text(paste.attachments.count == 1 ? "1 image" : "\(paste.attachments.count) images")
                        Text("·")
                    }
                    if let number { Text("#\(number)") }
                }
                .font(.caption)
                .foregroundStyle(.secondary)
            } else {
                Text("Nothing saved yet.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
    }

    private func preview(_ paste: CachedPaste) -> String {
        guard let text = paste.text, !text.isEmpty else { return "Images only" }
        return text
    }
}

private struct WorkspaceDetail: View {
    @ObservedObject var model: AppModel
    let back: () -> Void
    @State private var revoking: DeviceRecord?

    var body: some View {
        VStack(spacing: 0) {
            Button(action: back) {
                HStack(spacing: 6) {
                    Image(systemName: "chevron.left").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                    Text("Workspace & devices").font(.system(size: 13, weight: .semibold))
                    Spacer()
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .padding(.horizontal, 12)
            .padding(.vertical, 10)

            Divider()

            VStack(alignment: .leading, spacing: 12) {
                VStack(alignment: .leading, spacing: 3) {
                    Text("This Mac").font(.caption).foregroundStyle(.secondary)
                    Text(model.deviceName).font(.callout)
                }

                VStack(alignment: .leading, spacing: 6) {
                    Text("Connected devices").font(.caption).foregroundStyle(.secondary)
                    if model.devices.isEmpty {
                        Text("No other devices yet.").font(.callout).foregroundStyle(.secondary)
                    } else {
                        ForEach(model.devices) { device in
                            DeviceRow(device: device) { revoking = device }
                        }
                    }
                }

                Divider()

                ApproveDeviceSection(model: model)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(12)

            Spacer(minLength: 0)
        }
        .confirmationDialog(
            "Remove \(revoking?.displayName ?? "this device")?",
            isPresented: Binding(get: { revoking != nil }, set: { if !$0 { revoking = nil } }),
            titleVisibility: .visible
        ) {
            Button("Remove device", role: .destructive) {
                if let device = revoking {
                    Task { await model.revokeDevice(id: device.id) }
                }
                revoking = nil
            }
            Button("Cancel", role: .cancel) { revoking = nil }
        } message: {
            Text("It loses access to this workspace immediately.")
        }
    }
}

/// Approves a pairing request another device printed (e.g. `mcpaste setup` on a
/// connector host): enter its code, review who is asking, then approve or deny.
private struct ApproveDeviceSection: View {
    @ObservedObject var model: AppModel
    @State private var code = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Approve a device").font(.caption).foregroundStyle(.secondary)

            if let request = model.pendingApproval {
                VStack(alignment: .leading, spacing: 3) {
                    Text(request.proposedName).font(.callout)
                    Text("\(request.platform) · \(AppModel.pairingScopeLabel(request.requestedScope))")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                HStack(spacing: 8) {
                    Button("Approve") { Task { await model.approvePendingPairing() } }
                        .buttonStyle(.borderedProminent)
                        .controlSize(.small)
                    Button("Deny") { Task { await model.denyPendingPairing() } }
                        .controlSize(.small)
                    Spacer()
                    Button("Cancel") { model.dismissPairingApproval() }
                        .buttonStyle(.link)
                        .font(.caption)
                }
            } else {
                HStack(spacing: 6) {
                    TextField("Code from the other device", text: $code)
                        .textFieldStyle(.roundedBorder)
                        .controlSize(.small)
                        .onSubmit { lookUp() }
                    Button("Look up") { lookUp() }
                        .controlSize(.small)
                        .disabled(code.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }

            if let notice = model.pairingNotice {
                Text(notice)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private func lookUp() {
        Task {
            await model.lookupPairing(shortCode: code)
            if model.pendingApproval != nil { code = "" }
        }
    }
}

private struct DeviceRow: View {
    let device: DeviceRecord
    let revoke: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: device.platform == "macOS" ? "laptopcomputer" : "terminal")
                .foregroundStyle(.secondary)
            Text(device.displayName)
                .font(.callout)
                .lineLimit(1)
            Spacer(minLength: 6)
            if device.isCurrent {
                Text("This Mac").font(.caption).foregroundStyle(.tertiary)
            } else {
                Button("Remove", action: revoke)
                    .buttonStyle(.link)
                    .font(.caption)
            }
        }
    }
}

// MARK: - Shared popover pieces

struct StatusPill: View {
    let text: String
    let color: Color

    var body: some View {
        HStack(spacing: 5) {
            Circle().fill(color).frame(width: 6, height: 6)
            Text(text).font(.caption).foregroundStyle(.secondary)
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(.quaternary.opacity(0.6), in: Capsule())
        .accessibilityLabel("Sync status: \(text)")
    }
}

private struct InfoRow<Accessory: View>: View {
    let label: String
    let value: String
    var tint: Color = .primary
    @ViewBuilder var accessory: Accessory

    var body: some View {
        HStack(spacing: 8) {
            Text(label).font(.callout).foregroundStyle(.secondary)
            Spacer(minLength: 8)
            Text(value).font(.callout).foregroundStyle(tint)
            accessory
        }
    }
}

extension InfoRow where Accessory == EmptyView {
    init(label: String, value: String, tint: Color = .primary) {
        self.init(label: label, value: value, tint: tint) { EmptyView() }
    }
}

struct MenuRow: View {
    let title: String
    var prominent = false
    var chevron = false
    let action: () -> Void
    @State private var hovering = false

    var body: some View {
        Button(action: action) {
            HStack(spacing: 8) {
                Text(title)
                    .font(.callout)
                    .fontWeight(prominent ? .semibold : .regular)
                Spacer()
                if chevron {
                    Image(systemName: "chevron.right").font(.caption).foregroundStyle(.tertiary)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 7)
            .contentShape(Rectangle())
            .background(background, in: RoundedRectangle(cornerRadius: 6))
        }
        .buttonStyle(.plain)
        .onHover { hovering = $0 }
    }

    private var background: some ShapeStyle {
        if hovering { return AnyShapeStyle(Color.accentColor.opacity(0.18)) }
        if prominent { return AnyShapeStyle(Color.accentColor.opacity(0.12)) }
        return AnyShapeStyle(Color.clear)
    }
}

enum RelativeTime {
    static func string(for date: Date, now: Date = Date()) -> String {
        let seconds = Int(now.timeIntervalSince(date))
        switch seconds {
        case ..<5: return "just now"
        case ..<60: return "\(seconds) seconds ago"
        case ..<120: return "1 minute ago"
        case ..<3600: return "\(seconds / 60) minutes ago"
        case ..<7200: return "1 hour ago"
        case ..<86_400: return "\(seconds / 3600) hours ago"
        case ..<172_800: return "yesterday"
        default: return "\(seconds / 86_400) days ago"
        }
    }
}
