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
            EditorDetail(model: model)
        }
        .navigationTitle("MCPaste")
        .frame(minWidth: 720, minHeight: 460)
        .overlay {
            if isDropTargeted { DropTargetOverlay() }
        }
        .onDrop(of: [UTType.image.identifier], isTargeted: $isDropTargeted) { providers in
            handleImageProviders(providers)
            return true
        }
        .onPasteCommand(of: [UTType.image.identifier]) { providers in handleImageProviders(providers) }
    }

    private func handleImageProviders(_ providers: [NSItemProvider]) {
        Task {
            let data = await loadImageData(from: providers)
            if !data.isEmpty { await model.uploadImages(data) }
        }
    }

    private func loadImageData(from providers: [NSItemProvider]) async -> [Data] {
        var result: [Data] = []
        for provider in providers {
            if let data = await loadData(from: provider) { result.append(data) }
        }
        return result
    }

    private func loadData(from provider: NSItemProvider) async -> Data? {
        await withCheckedContinuation { continuation in
            provider.loadDataRepresentation(forTypeIdentifier: UTType.image.identifier) { data, _ in
                continuation.resume(returning: data)
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
                    Text("No saved pastes yet. Write something and press Save.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .lineLimit(nil)
                        .fixedSize(horizontal: false, vertical: true)
                        .listRowSeparator(.hidden)
                } else {
                    ForEach(filtered, id: \.id) { paste in
                        HistoryRow(paste: paste)
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
                model.selectPaste(paste)
            }
        )
    }
}

private struct HistoryRow: View {
    let paste: CachedPaste

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title)
                .font(.system(size: 12))
                .lineLimit(2)
                .foregroundStyle(paste.deleted ? .secondary : .primary)
            HStack(spacing: 5) {
                Text("#\(paste.sequence)")
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
                    Text(model.devices.isEmpty ? "This Mac" : "\(model.devices.count) devices")
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
            PasteEditor(model: model)
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
                Text(subtitle).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            StatusPill(text: syncText, color: syncColor)
            Button {
                model.startNewPaste()
            } label: {
                Image(systemName: "square.and.pencil")
            }
            .help("New paste (⌘N)")
            .keyboardShortcut("n", modifiers: .command)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
    }

    private var title: String {
        guard let paste = model.selectedPaste else { return "New paste" }
        return "Paste #\(paste.sequence)"
    }

    private var subtitle: String {
        if model.selectedPaste == nil { return "Not saved yet" }
        guard let date = model.lastSyncedAt else { return "Saved" }
        return "Synced \(RelativeTime.string(for: date))"
    }

    private var syncText: String {
        if model.syncFailed { return "Offline" }
        if model.pending || model.pendingCount > 0 { return "Syncing" }
        return model.syncStatus == .notConnected ? "Connecting" : "Synced"
    }

    private var syncColor: Color {
        if model.syncFailed { return .red }
        if model.pending || model.pendingCount > 0 { return .orange }
        return model.syncStatus == .notConnected ? .secondary : .green
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

    var body: some View {
        ZStack(alignment: .topLeading) {
            TextEditor(text: $model.draft)
                .font(.system(size: 12, design: .monospaced))
                .scrollContentBackground(.hidden)
                .background(Color(nsColor: .textBackgroundColor))
                .padding(.horizontal, 8)
                .padding(.vertical, 10)
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
            }
            if model.attachments.count < ImageNormalizer.maxAttachmentItems {
                DropHint(empty: model.attachments.isEmpty)
            }
            Spacer(minLength: 8)
            Text("\(model.attachments.count) of \(ImageNormalizer.maxAttachmentItems) images")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
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
            Button("Delete", role: .destructive) {
                Task { await model.deleteCurrentPaste() }
            }
            .disabled(!model.canDeleteCurrentPaste)
            .padding(.trailing, 8)
            Button("Save") {
                Task { await model.saveDraft() }
            }
            .buttonStyle(.borderedProminent)
            .keyboardShortcut(.defaultAction)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
    }

    private var statistics: String {
        let lines = model.draft.isEmpty ? 0 : model.draft.components(separatedBy: "\n").count
        return "Exact text preserved · \(lines) lines · \(model.draft.count) characters"
    }
}

private struct DropTargetOverlay: View {
    var body: some View {
        ZStack {
            Rectangle().fill(.background.opacity(0.82))
            RoundedRectangle(cornerRadius: 12)
                .strokeBorder(Color.accentColor, style: StrokeStyle(lineWidth: 2, dash: [8]))
                .padding(16)
            Label("Drop images to attach", systemImage: "photo.badge.plus")
                .font(.title3)
                .foregroundStyle(Color.accentColor)
        }
        .allowsHitTesting(false)
    }
}
