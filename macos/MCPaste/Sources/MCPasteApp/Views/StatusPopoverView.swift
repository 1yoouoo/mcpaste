import AppKit
import MCPasteCore
import SwiftUI

struct StatusPopoverView: View {
    @ObservedObject var model: AppModel
    @Environment(\.openWindow) private var openWindow
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Text("MCPaste")
                    .font(.callout.weight(.semibold))
                Spacer(minLength: 8)
                StatusPill(state: model.syncState)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)

            if let message = model.errorMessage {
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
            DeviceSection(devices: model.devices)

            if !model.connectorNames.isEmpty {
                Divider()
                ConnectorSection(names: model.connectorNames)
            }

            Divider()
            VStack(spacing: 1) {
                MenuRow(title: "Open Context Window", prominent: true) {
                    ContentWindowOpener.open(
                        openWindow: { openWindow(id: "content") },
                        activate: ContentWindowOpener.activateApp,
                        dismissPopover: { dismiss() }
                    )
                }
                MenuRow(title: "Quit MCPaste") {
                    NSApplication.shared.terminate(nil)
                }
            }
            .padding(6)
        }
        .frame(width: 300)
    }
}

private struct DeviceSection: View {
    let devices: [PeerDevice]

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            Text("Connected devices")
                .font(.caption)
                .foregroundStyle(.secondary)
            if devices.isEmpty {
                Text("Waiting for nearby devices")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .padding(.vertical, 3)
            } else {
                ScrollView(.vertical) {
                    LazyVStack(spacing: 7) {
                        ForEach(devices) { device in
                            DeviceRow(device: device)
                        }
                    }
                }
                .frame(maxHeight: 150)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
    }
}

struct DeviceRowPresentation {
    let highlightsLocalDevice: Bool
    let showsSourceBadge: Bool
    let reachable: Bool
    let accessibilityLabel: String

    init(_ device: PeerDevice) {
        highlightsLocalDevice = device.isLocal
        showsSourceBadge = device.isSource
        reachable = device.reachable
        var traits: [String] = []
        if device.isLocal { traits.append("Local device") }
        if device.isSource { traits.append("Current source") }
        traits.append(device.reachable ? "Reachable" : "Unavailable")
        accessibilityLabel = ([device.displayName] + traits).joined(separator: ", ")
    }
}

private struct DeviceRow: View {
    let device: PeerDevice

    var body: some View {
        let presentation = DeviceRowPresentation(device)
        HStack(spacing: 8) {
            ZStack(alignment: .bottomTrailing) {
                Image(systemName: device.isLocal ? "laptopcomputer" : "desktopcomputer")
                    .font(.callout)
                    .foregroundStyle(device.isLocal ? Color.accentColor : Color.secondary)
                    .frame(width: 18)
                if presentation.showsSourceBadge {
                    Circle()
                        .fill(.green)
                        .frame(width: 6, height: 6)
                        .overlay(Circle().stroke(Color(nsColor: .windowBackgroundColor), lineWidth: 1))
                        .accessibilityLabel("Current context source")
                }
            }
            Text(device.displayName)
                .font(.callout)
                .lineLimit(1)
                .truncationMode(.middle)
            Spacer(minLength: 8)
            Circle()
                .fill(presentation.reachable ? Color.green : Color.gray)
                .frame(width: 6, height: 6)
                .accessibilityLabel(presentation.reachable ? "Reachable" : "Unavailable")
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 6)
        .background(
            presentation.highlightsLocalDevice ? Color.accentColor.opacity(0.10) : Color.clear,
            in: RoundedRectangle(cornerRadius: 6)
        )
        .opacity(presentation.reachable ? 1 : 0.55)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(presentation.accessibilityLabel)
    }
}

private struct ConnectorSection: View {
    let names: [String]

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            Text("AI tool connections")
                .font(.caption)
                .foregroundStyle(.secondary)
            ForEach(names, id: \.self) { name in
                HStack(spacing: 8) {
                    ConnectorIconView(name: name)
                    Text(name)
                        .font(.callout)
                        .lineLimit(1)
                    Spacer(minLength: 0)
                }
                .padding(.horizontal, 8)
                .padding(.vertical, 3)
                .accessibilityLabel("Configured AI tool: \(name)")
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
    }
}

struct StatusPill: View {
    let state: PeerSyncState

    var body: some View {
        HStack(spacing: 5) {
            Circle()
                .fill(color)
                .frame(width: 6, height: 6)
            Text(EditorStatus.text(for: state))
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(.quaternary.opacity(0.6), in: Capsule())
        .accessibilityLabel("Sync status: \(EditorStatus.text(for: state))")
    }

    private var color: Color {
        switch state {
        case .upToDate: return .green
        case .updating: return .orange
        case .waitingToSync: return .secondary
        case .sourceOffline: return .orange
        }
    }
}

private struct MenuRow: View {
    let title: String
    var prominent = false
    let action: () -> Void
    @State private var hovering = false

    var body: some View {
        Button(action: action) {
            HStack(spacing: 8) {
                Text(title)
                    .font(.callout)
                    .fontWeight(prominent ? .semibold : .regular)
                Spacer()
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
        let seconds = max(0, Int(now.timeIntervalSince(date)))
        switch seconds {
        case ..<5: return "just now"
        case ..<60: return "\(seconds) seconds ago"
        case ..<120: return "1 minute ago"
        case ..<3_600: return "\(seconds / 60) minutes ago"
        case ..<7_200: return "1 hour ago"
        case ..<86_400: return "\(seconds / 3_600) hours ago"
        case ..<172_800: return "yesterday"
        default: return "\(seconds / 86_400) days ago"
        }
    }
}
