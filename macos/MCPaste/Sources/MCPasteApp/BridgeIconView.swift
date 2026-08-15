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

    static func menuBarImage() -> NSImage? {
        guard let url = Bundle.module.url(
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
