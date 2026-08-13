import SwiftUI

struct DeviceListView: View {
    var body: some View {
        VStack(alignment: .leading) {
            Text("Devices").font(.headline)
            Text("Manage connected devices from the workspace settings.").foregroundStyle(.secondary)
        }.padding()
    }
}
