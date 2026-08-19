import AppKit
import SwiftUI

struct ConnectorIconView: View {
    let name: String

    var body: some View {
        if let image = Self.icon(for: name) {
            Image(nsImage: image)
                .renderingMode(.template)
                .resizable()
                .aspectRatio(contentMode: .fit)
                .frame(width: 18, height: 18)
                .foregroundStyle(.secondary)
        } else {
            Image(systemName: "cable.connector.horizontal")
                .font(.callout)
                .foregroundStyle(.secondary)
                .frame(width: 18)
        }
    }

    static func resourceName(for connectorName: String) -> String? {
        switch connectorName {
        case "Codex": return "openai"
        case "Claude Code": return "claude"
        default: return nil
        }
    }

    static func icon(for connectorName: String) -> NSImage? {
        guard let resourceName = resourceName(for: connectorName),
              let url = BridgeIconView.resourceBundle().url(
                  forResource: resourceName,
                  withExtension: "svg"
              ),
              let image = NSImage(contentsOf: url) else {
            return nil
        }

        image.isTemplate = true
        image.size = NSSize(width: 18, height: 18)
        return image
    }
}
