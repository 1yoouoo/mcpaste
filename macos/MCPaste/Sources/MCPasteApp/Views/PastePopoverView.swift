import SwiftUI
import UniformTypeIdentifiers

struct PastePopoverView: View {
    @ObservedObject var model: AppModel
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack { Text("Paste").font(.headline); Spacer(); if model.pending { ProgressView().controlSize(.small).accessibilityLabel("Pending sync") } }
            TextEditor(text: $model.draft).font(.body).frame(minHeight: 150).accessibilityLabel("Paste text")
            HStack { Text("Exact text is preserved").font(.caption).foregroundStyle(.secondary); Spacer(); Button("Save") { Task { await model.saveDraft() } }.keyboardShortcut(.defaultAction) }
            Button("Delete current paste", role: .destructive) { Task { await model.deleteCurrentPaste() } }
        }
        .padding(14)
        .frame(width: 360)
        .onDrop(of: [UTType.image.identifier], isTargeted: nil) { providers in
            Task {
                let data = await loadImageData(from: providers)
                if !data.isEmpty { await model.uploadImages(data) }
            }
            return true
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
