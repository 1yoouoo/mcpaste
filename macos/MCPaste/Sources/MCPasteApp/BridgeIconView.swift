import AppKit
import SwiftUI

struct BridgeIconView: View {
    var body: some View {
        if let image = Self.menuBarImage() {
            Image(nsImage: image)
                .renderingMode(.template)
                .resizable()
                .aspectRatio(contentMode: .fit)
                .frame(width: 18, height: 18)
        } else {
            Image(systemName: "doc.on.clipboard")
        }
    }

    // Bundle.module traps when the resource bundle is absent, and in the
    // released .app it only searches the bundle root and the CI build path.
    // Look in the app's Resources directory first and fall back to
    // Bundle.module for `swift run` and `swift test` builds.
    static func resourceBundle() -> Bundle {
        for candidate in [Bundle.main.resourceURL, Bundle.main.bundleURL] {
            if let url = candidate?.appendingPathComponent("MCPaste_MCPasteApp.bundle"),
               let bundle = Bundle(url: url) {
                return bundle
            }
        }
        return .module
    }

    static func menuBarImage() -> NSImage? {
        guard let url = resourceBundle().url(
            forResource: "bridge-one-svgrepo-com",
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
