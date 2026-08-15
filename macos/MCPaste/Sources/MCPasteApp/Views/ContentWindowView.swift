import SwiftUI
import UniformTypeIdentifiers

struct PastePopoverView: View {
    @ObservedObject var model: AppModel
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack { Text("Paste").font(.headline); Spacer(); if model.pending || model.pendingCount > 0 { ProgressView().controlSize(.small).accessibilityLabel("Pending sync") } }
            TextEditor(text: $model.draft).font(.body).frame(minHeight: 150).accessibilityLabel("Paste text")
            if !model.history.isEmpty {
                Text("Recent history").font(.subheadline)
                ForEach(model.history.prefix(5), id: \.id) { paste in
                    Button(action: { model.selectPaste(paste) }) {
                        HStack { Text(paste.deleted ? "Deleted paste" : (paste.text ?? "Image paste")).lineLimit(1); Spacer(); Text("#\(paste.sequence)").font(.caption).foregroundStyle(.secondary) }
                    }.buttonStyle(.plain).disabled(paste.deleted)
                }
            }
            HStack { Text("Exact text is preserved").font(.caption).foregroundStyle(.secondary); Spacer(); Button("Save") { Task { await model.saveDraft() } }.keyboardShortcut(.defaultAction) }
            Button("Delete current paste", role: .destructive) { Task { await model.deleteCurrentPaste() } }
            if !model.devices.isEmpty {
                Text("Devices").font(.subheadline)
                ForEach(model.devices) { device in
                    HStack { Text(device.displayName).lineLimit(1); Spacer(); Text(device.isCurrent ? "This Mac" : device.platform).font(.caption).foregroundStyle(.secondary) }
                }
            }
        }
        .padding(14)
        .frame(width: 360)
        .onDrop(of: [UTType.image.identifier], isTargeted: nil) { providers in
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
