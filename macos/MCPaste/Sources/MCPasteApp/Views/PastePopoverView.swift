import SwiftUI

struct PastePopoverView: View {
    @ObservedObject var model: AppModel
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack { Text("Paste").font(.headline); Spacer(); if model.pending { ProgressView().controlSize(.small).accessibilityLabel("Pending sync") } }
            TextEditor(text: $model.draft).font(.body).frame(minHeight: 150).accessibilityLabel("Paste text")
            HStack { Text("Exact text is preserved").font(.caption).foregroundStyle(.secondary); Spacer(); Button("Save") { Task { await model.saveDraft() } }.keyboardShortcut(.defaultAction) }
            Button("Delete current paste", role: .destructive) { Task { await model.deleteCurrentPaste() } }
        }.padding(14).frame(width: 360)
    }
}
