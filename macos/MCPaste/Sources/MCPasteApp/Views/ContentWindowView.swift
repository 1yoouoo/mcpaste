import AppKit
import MCPasteCore
import SwiftUI
import UniformTypeIdentifiers

struct ContentWindowView: View {
    @ObservedObject var model: AppModel
    @State private var isDropTargeted = false

    var body: some View {
        VStack(spacing: 0) {
            DetailHeader(model: model)
            Divider()
            if let message = model.errorMessage {
                ErrorBanner(message: message)
                Divider()
            }
            ContextEditor(model: model, isDropTargeted: $isDropTargeted)
            Divider()
            AttachmentStrip(model: model)
            Divider()
            DetailFooter(model: model)
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
                newPaste: { Task { await model.clearContext() } }
            )
        }
        .onDisappear {
            ImagePasteMonitor.shared.unsubscribe()
        }
    }

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
                } else if
                    let data = await load(provider, as: UTType.utf8PlainText.identifier),
                    let contents = String(data: data, encoding: .utf8)
                {
                    text += contents
                }
            }
            if !images.isEmpty { await model.uploadImages(images) }
            if !text.isEmpty { model.draft += text }
        }
    }

    private func imageData(at url: URL) -> Data? {
        guard let type = UTType(filenameExtension: url.pathExtension), type.conforms(to: .image) else {
            return nil
        }
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
                    continuation.resume(returning: nil)
                    return
                }
                continuation.resume(returning: URL(string: text))
            }
        }
    }
}

private struct DetailHeader: View {
    @ObservedObject var model: AppModel

    var body: some View {
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 1) {
                Text("Current context")
                    .font(.system(size: 13, weight: .semibold))
                TimelineView(.periodic(from: .now, by: 10)) { context in
                    Text(
                        EditorStatus.subtitle(
                            lastUpdatedBy: model.lastUpdatedBy,
                            lastUpdatedAt: model.lastUpdatedAt,
                            now: context.date
                        )
                    )
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                }
            }
            Spacer(minLength: 12)
            StatusPill(state: model.syncState)
            Button {
                Task { await model.clearContext() }
            } label: {
                Image(systemName: "square.and.pencil")
            }
            .buttonStyle(.borderless)
            .controlSize(.regular)
            .keyboardShortcut("n", modifiers: .command)
            .help("Start a blank shared context (⌘N)")
            .accessibilityLabel(BlankContextAction.accessibilityLabel)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
    }
}

private struct ErrorBanner: View {
    let message: String

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(systemName: "exclamationmark.triangle.fill")
                .font(.caption)
                .foregroundStyle(.orange)
            Text(message)
                .font(.callout)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
        .background(Color.orange.opacity(0.10))
    }
}

private struct ContextEditor: View {
    @ObservedObject var model: AppModel
    @Binding var isDropTargeted: Bool

    var body: some View {
        ZStack(alignment: .topLeading) {
            PasteTextEditor(text: $model.draft, isDropTargeted: $isDropTargeted) { images in
                Task { await model.uploadImages(images) }
            }
            .accessibilityLabel("Current context text")
            if model.draft.isEmpty {
                Text("Write or paste text here…")
                    .font(.system(size: 12, design: .monospaced))
                    .foregroundStyle(.tertiary)
                    .padding(.horizontal, PasteTextEditorLayout.placeholderHorizontalPadding)
                    .padding(.vertical, PasteTextEditorLayout.placeholderVerticalPadding)
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
                .disabled(model.uploadingCount > 0)
            }
            ForEach(0..<model.uploadingCount, id: \.self) { _ in
                ArrivingThumbnail()
            }
            if model.attachments.count + model.uploadingCount < ImageNormalizer.maxAttachmentItems {
                DropHint(empty: model.attachments.isEmpty && model.uploadingCount == 0)
            }
            Spacer(minLength: 8)
            Text(countLabel)
                .font(.caption)
                .foregroundStyle(model.uploadingCount > 0 ? Color.accentColor : Color.secondary)
                .animation(.default, value: model.uploadingCount)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
    }

    private var countLabel: String {
        if model.uploadingCount > 0 {
            return model.uploadingCount == 1 ? "Adding 1 image…" : "Adding \(model.uploadingCount) images…"
        }
        return "\(model.attachments.count) of \(ImageNormalizer.maxAttachmentItems) images"
    }
}

private struct ArrivingThumbnail: View {
    @State private var pulsing = false

    var body: some View {
        RoundedRectangle(cornerRadius: 5)
            .fill(.quaternary)
            .frame(width: 44, height: 32)
            .overlay(ProgressView().controlSize(.small).scaleEffect(0.7))
            .overlay(
                RoundedRectangle(cornerRadius: 5)
                    .strokeBorder(Color.accentColor.opacity(pulsing ? 0.7 : 0.25))
            )
            .onAppear {
                withAnimation(.easeInOut(duration: 0.9).repeatForever(autoreverses: true)) {
                    pulsing = true
                }
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
            Image(systemName: "text.alignleft")
                .font(.caption2)
                .foregroundStyle(.tertiary)
            Text(statistics)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            Spacer(minLength: 12)
            Text(ConnectorConfigurationCopy.text(count: model.connectorNames.count))
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            Label("Shared automatically", systemImage: "arrow.triangle.2.circlepath")
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
    }

    private var statistics: String {
        let lines = model.draft.isEmpty ? 0 : model.draft.components(separatedBy: "\n").count
        return "Exact text preserved · \(lines) lines · \(model.draft.count) characters"
    }
}

enum ConnectorConfigurationCopy {
    static func text(count: Int) -> String {
        count == 1 ? "1 MCP connector configured" : "\(count) MCP connectors configured"
    }
}

enum BlankContextAction {
    static let accessibilityLabel = "Start a blank shared context"
}

enum EditorStatus {
    static func text(for state: PeerSyncState) -> String {
        switch state {
        case .upToDate: return "Up to date"
        case .updating: return "Updating…"
        case .waitingToSync: return "Waiting to sync"
        case .sourceOffline: return "Source offline"
        }
    }

    static func subtitle(lastUpdatedBy: String?, lastUpdatedAt: Date?, now: Date) -> String {
        guard let lastUpdatedBy, let lastUpdatedAt else { return "Changes sync automatically" }
        return "Updated from \(lastUpdatedBy) · \(RelativeTime.string(for: lastUpdatedAt, now: now))"
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
