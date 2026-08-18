import SwiftUI
import UniformTypeIdentifiers
import AppKit
import MCPasteCore

struct ContentWindowView: View {
    @ObservedObject var model: AppModel
    @State private var isDropTargeted = false
    @State private var columnVisibility: NavigationSplitViewVisibility = .all

    var body: some View {
        NavigationSplitView(columnVisibility: $columnVisibility) {
            HistorySidebar(model: model)
                .navigationSplitViewColumnWidth(min: 200, ideal: 232, max: 320)
        } detail: {
            EditorDetail(model: model, isDropTargeted: $isDropTargeted)
        }
        .navigationTitle("MCPaste")
        .frame(minWidth: 720, minHeight: 460)
        .overlay {
            if isDropTargeted { DropTargetOverlay() }
        }
        .onDrop(of: DroppedContent.acceptedTypes.map(\.rawValue), isTargeted: $isDropTargeted) { providers in
            handleProviders(providers)
            return true
        }
        .onAppear {
            ImagePasteMonitor.shared.subscribe(
                paste: { images in Task { await model.uploadImages(images) } },
                newPaste: { Task { await model.startNewPaste() } }
            )
        }
        .onDisappear {
            ImagePasteMonitor.shared.unsubscribe()
            Task { await model.saveIfNeeded() }
        }
    }

    /// A drop anywhere in the window: images attach, text lands in the editor.
    private func handleProviders(_ providers: [NSItemProvider]) {
        Task {
            var images: [Data] = []
            var text = ""
            for provider in providers {
                if let data = await load(provider, as: UTType.image.identifier) {
                    images.append(data)
                } else if let url = await loadFileURL(provider) {
                    if let data = imageData(at: url) {
                        images.append(data)
                    } else if let contents = try? String(contentsOf: url, encoding: .utf8) {
                        text += contents
                    }
                } else if let data = await load(provider, as: UTType.utf8PlainText.identifier),
                          let contents = String(data: data, encoding: .utf8) {
                    text += contents
                }
            }
            if !images.isEmpty { await model.uploadImages(images) }
            if !text.isEmpty { model.draft += text }
        }
    }

    private func imageData(at url: URL) -> Data? {
        guard let type = UTType(filenameExtension: url.pathExtension), type.conforms(to: .image) else { return nil }
        return try? Data(contentsOf: url)
    }

    private func load(_ provider: NSItemProvider, as identifier: String) async -> Data? {
        guard provider.hasItemConformingToTypeIdentifier(identifier) else { return nil }
        return await withCheckedContinuation { continuation in
            provider.loadDataRepresentation(forTypeIdentifier: identifier) { data, _ in
                continuation.resume(returning: data)
            }
        }
    }

    private func loadFileURL(_ provider: NSItemProvider) async -> URL? {
        guard provider.hasItemConformingToTypeIdentifier(UTType.fileURL.identifier) else { return nil }
        return await withCheckedContinuation { continuation in
            provider.loadDataRepresentation(forTypeIdentifier: UTType.fileURL.identifier) { data, _ in
                guard let data, let text = String(data: data, encoding: .utf8) else {
                    return continuation.resume(returning: nil)
                }
                continuation.resume(returning: URL(string: text))
            }
        }
    }
}

// MARK: - Sidebar

private struct HistorySidebar: View {
    @ObservedObject var model: AppModel
    @State private var query = ""

    var body: some View {
        VStack(spacing: 0) {
            List(selection: selectionBinding) {
                if model.history.isEmpty {
                    Text(EditorStatus.emptyHistory)
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .lineLimit(nil)
                        .fixedSize(horizontal: false, vertical: true)
                        .listRowSeparator(.hidden)
                } else {
                    ForEach(filtered, id: \.id) { paste in
                        HistoryRow(paste: paste, number: model.displayNumber(for: paste))
                            .tag(paste.id)
                    }
                }
            }
            .listStyle(.sidebar)
            .searchable(text: $query, placement: .sidebar, prompt: "Search history")

            Divider()
            DeviceFooter(model: model)
        }
    }

    private var filtered: [CachedPaste] {
        guard !query.isEmpty else { return model.history }
        return model.history.filter { ($0.text ?? "").localizedCaseInsensitiveContains(query) }
    }

    private var selectionBinding: Binding<String?> {
        Binding(
            get: { model.selectedPasteID },
            set: { id in
                guard let id, let paste = model.history.first(where: { $0.id == id }) else { return }
                Task { await model.selectPaste(paste) }
            }
        )
    }
}

private struct HistoryRow: View {
    let paste: CachedPaste
    let number: Int

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title)
                .font(.system(size: 12))
                .lineLimit(2)
                .foregroundStyle(paste.deleted ? .secondary : .primary)
            HStack(spacing: 5) {
                Text("#\(number)")
                if !paste.attachments.isEmpty {
                    Text("·")
                    Image(systemName: "photo").font(.system(size: 9))
                    Text("\(paste.attachments.count)")
                }
                if paste.deleted {
                    Text("·")
                    Text("Deleted")
                }
            }
            .font(.caption2)
            .foregroundStyle(.secondary)
        }
        .padding(.vertical, 3)
    }

    private var title: String {
        if paste.deleted { return "Deleted paste" }
        guard let text = paste.text, !text.isEmpty else { return "Images only" }
        return text
    }
}

private struct DeviceFooter: View {
    @ObservedObject var model: AppModel
    @State private var expanded = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Button {
                expanded.toggle()
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: "laptopcomputer.and.iphone").font(.caption).foregroundStyle(.secondary)
                    Text(AppModel.deviceCountLabel(for: model.devices))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Spacer()
                    Image(systemName: expanded ? "chevron.down" : "chevron.up")
                        .font(.caption2)
                        .foregroundStyle(.tertiary)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)

            if expanded {
                ForEach(model.devices) { device in
                    HStack(spacing: 6) {
                        Text(device.displayName).font(.caption).lineLimit(1)
                        Spacer(minLength: 4)
                        Text(device.isCurrent ? "This Mac" : device.platform)
                            .font(.caption2)
                            .foregroundStyle(.tertiary)
                    }
                }
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }
}

// MARK: - Detail

private struct EditorDetail: View {
    @ObservedObject var model: AppModel
    @Binding var isDropTargeted: Bool

    var body: some View {
        VStack(spacing: 0) {
            DetailHeader(model: model)
            Divider()
            if let message = model.errorMessage {
                ErrorBanner(message: message) {
                    Task { await model.retryNow() }
                }
                Divider()
            }
            PasteEditor(model: model, isDropTargeted: $isDropTargeted)
            Divider()
            AttachmentStrip(model: model)
            Divider()
            DetailFooter(model: model)
        }
    }
}

private struct DetailHeader: View {
    @ObservedObject var model: AppModel

    var body: some View {
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 1) {
                Text(title).font(.system(size: 13, weight: .semibold))
                // Recomputed on a schedule so "Synced 12 seconds ago" does not freeze.
                TimelineView(.periodic(from: .now, by: 10)) { context in
                    Text(subtitle(now: context.date)).font(.caption).foregroundStyle(.secondary)
                }
            }
            Spacer()
            StatusPill(text: pill.text, color: pill.color)
            Button {
                Task { await model.startNewPaste() }
            } label: {
                Image(systemName: "square.and.pencil")
            }
            .help("New paste (⌘N)")
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
    }

    private var title: String {
        guard let paste = model.selectedPaste else { return "New paste" }
        return "Paste #\(model.displayNumber(for: paste))"
    }

    private func subtitle(now: Date) -> String {
        EditorStatus.headerSubtitle(
            hasSavedSelection: model.selectedPaste != nil,
            lastSyncedAt: model.lastSyncedAt,
            now: now
        )
    }

    private var pill: (text: String, color: Color) {
        EditorStatus.pill(
            syncFailed: model.syncFailed,
            syncing: model.pending || model.pendingCount > 0,
            connected: model.syncStatus != .notConnected
        )
    }
}

private struct ErrorBanner: View {
    let message: String
    let dismiss: () -> Void

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(systemName: "exclamationmark.triangle.fill")
                .font(.caption)
                .foregroundStyle(.orange)
            Text(message)
                .font(.callout)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 8)
            Button("Dismiss", action: dismiss)
                .buttonStyle(.link)
                .font(.callout)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
        .background(Color.orange.opacity(0.10))
    }
}

private struct PasteEditor: View {
    @ObservedObject var model: AppModel
    @Binding var isDropTargeted: Bool

    var body: some View {
        ZStack(alignment: .topLeading) {
            PasteTextEditor(text: $model.draft, isDropTargeted: $isDropTargeted) { images in
                Task { await model.uploadImages(images) }
            }
            .accessibilityLabel("Paste text")
            if model.draft.isEmpty {
                Text("Write or paste text here…")
                    .font(.system(size: 12, design: .monospaced))
                    .foregroundStyle(.tertiary)
                    .padding(.horizontal, 13)
                    .padding(.vertical, 18)
                    .allowsHitTesting(false)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color(nsColor: .textBackgroundColor))
    }
}

private struct AttachmentStrip: View {
    @ObservedObject var model: AppModel

    var body: some View {
        HStack(spacing: 8) {
            ForEach(Array(model.attachments.enumerated()), id: \.offset) { index, image in
                AttachmentThumbnail(image: image) {
                    Task { await model.removeAttachment(at: index) }
                }
                .disabled(model.isUploading)
            }
            ForEach(0..<model.uploadingCount, id: \.self) { _ in
                ArrivingThumbnail()
            }
            if model.attachments.count + model.uploadingCount < ImageNormalizer.maxAttachmentItems {
                DropHint(empty: model.attachments.isEmpty && !model.isUploading)
            }
            Spacer(minLength: 8)
            Text(countLabel)
                .font(.caption)
                .foregroundStyle(model.isUploading ? Color.accentColor : Color.secondary)
                .animation(.default, value: model.uploadingCount)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
    }

    private var countLabel: String {
        if model.isUploading {
            return model.uploadingCount == 1 ? "Adding 1 image…" : "Adding \(model.uploadingCount) images…"
        }
        return "\(model.attachments.count) of \(ImageNormalizer.maxAttachmentItems) images"
    }
}

/// Placeholder for an image that has been accepted but is still being prepared or sent.
private struct ArrivingThumbnail: View {
    @State private var pulsing = false

    var body: some View {
        RoundedRectangle(cornerRadius: 5)
            .fill(.quaternary)
            .frame(width: 44, height: 32)
            .overlay(ProgressView().controlSize(.small).scaleEffect(0.7))
            .overlay(RoundedRectangle(cornerRadius: 5).strokeBorder(Color.accentColor.opacity(pulsing ? 0.7 : 0.25)))
            .onAppear {
                withAnimation(.easeInOut(duration: 0.9).repeatForever(autoreverses: true)) { pulsing = true }
            }
            .accessibilityLabel("Image being added")
    }
}

private struct AttachmentThumbnail: View {
    let image: NormalizedImage
    let remove: () -> Void
    @State private var hovering = false

    var body: some View {
        ZStack(alignment: .topTrailing) {
            Group {
                if let nsImage = NSImage(data: image.data) {
                    Image(nsImage: nsImage)
                        .resizable()
                        .aspectRatio(contentMode: .fill)
                } else {
                    Rectangle().fill(.quaternary)
                }
            }
            .frame(width: 44, height: 32)
            .clipShape(RoundedRectangle(cornerRadius: 5))
            .overlay(RoundedRectangle(cornerRadius: 5).strokeBorder(.quaternary))

            if hovering {
                Button(action: remove) {
                    Image(systemName: "xmark.circle.fill")
                        .font(.system(size: 12))
                        .foregroundStyle(.white, .black.opacity(0.6))
                }
                .buttonStyle(.plain)
                .offset(x: 5, y: -5)
                .help("Remove image")
            }
        }
        .onHover { hovering = $0 }
        .accessibilityLabel("Attached image, \(image.width) by \(image.height)")
    }
}

private struct DropHint: View {
    let empty: Bool

    var body: some View {
        HStack(spacing: 6) {
            RoundedRectangle(cornerRadius: 5)
                .strokeBorder(style: StrokeStyle(lineWidth: 1, dash: [3]))
                .foregroundStyle(.tertiary)
                .frame(width: 44, height: 32)
                .overlay(Image(systemName: "plus").font(.caption).foregroundStyle(.secondary))
            if empty {
                Text("Drop or paste images")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }
}

private struct DetailFooter: View {
    @ObservedObject var model: AppModel

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "text.alignleft").font(.caption2).foregroundStyle(.tertiary)
            Text(statistics)
                .font(.caption)
                .foregroundStyle(.secondary)
            Spacer()
            Label(saveState.text, systemImage: saveState.icon)
                .font(.caption)
                .foregroundStyle(saveState.tint)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
    }

    private var statistics: String {
        let lines = model.draft.isEmpty ? 0 : model.draft.components(separatedBy: "\n").count
        return "Exact text preserved · \(lines) lines · \(model.draft.count) characters"
    }

    private var saveState: (text: String, icon: String, tint: Color) {
        let state = EditorStatus.footer(
            saving: model.pending || model.isUploading,
            connectorCount: AppModel.mcpConnectorCount(in: model.devices)
        )
        return (state.text, state.icon, .secondary)
    }
}

/// Pure descriptions of the editor's status line. Everything saves automatically,
/// so the header and footer report what the user cannot otherwise see: the network
/// connection and the MCP connectors linked to this workspace.
enum EditorStatus {
    static let emptyHistory = "No pastes yet. Write something — it saves automatically."

    static func pill(syncFailed: Bool, syncing: Bool, connected: Bool) -> (text: String, color: Color) {
        if syncFailed { return ("Offline", .red) }
        if syncing { return ("Syncing", .orange) }
        return connected ? ("Online", .green) : ("Connecting", .secondary)
    }

    static func headerSubtitle(hasSavedSelection: Bool, lastSyncedAt: Date?, now: Date) -> String {
        guard hasSavedSelection else { return "Saves automatically" }
        guard let lastSyncedAt else { return "Saved" }
        return "Synced \(RelativeTime.string(for: lastSyncedAt, now: now))"
    }

    static func footer(saving: Bool, connectorCount: Int) -> (text: String, icon: String) {
        if saving { return ("Saving…", "arrow.triangle.2.circlepath") }
        switch connectorCount {
        case 0: return ("No MCP connector linked yet", "cable.connector.horizontal")
        case 1: return ("1 MCP connector linked", "cable.connector.horizontal")
        default: return ("\(connectorCount) MCP connectors linked", "cable.connector.horizontal")
        }
    }
}

private struct DropTargetOverlay: View {
    var body: some View {
        ZStack {
            Rectangle().fill(.background.opacity(0.82))
            RoundedRectangle(cornerRadius: 12)
                .strokeBorder(Color.accentColor, style: StrokeStyle(lineWidth: 2, dash: [8]))
                .padding(16)
            Label("Drop images or text", systemImage: "square.and.arrow.down")
                .font(.title3)
                .foregroundStyle(Color.accentColor)
        }
        .allowsHitTesting(false)
    }
}
