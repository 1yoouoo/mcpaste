import AppKit
import MCPasteCore
import SwiftUI

struct RuntimeOwner {
    let start: () async throws -> any PeerRuntimeServing
    let stop: () async -> Void

    static func live() -> RuntimeOwner {
        guard let cliURL = ConnectorSetup.embeddedCLIURL() else {
            return RuntimeOwner(
                start: { throw PeerRuntimeProcessError.launchFailed },
                stop: {}
            )
        }
        let process = PeerRuntimeProcess(cliURL: cliURL)
        return RuntimeOwner(
            start: { try await process.start() },
            stop: { await process.stop() }
        )
    }
}

@MainActor
final class AppLifecycleController {
    private let runtimeOwner: RuntimeOwner
    private var startTask: Task<any PeerRuntimeServing, Error>?
    private var terminated = false
    private var stopped = false

    init(runtimeOwner: RuntimeOwner) {
        self.runtimeOwner = runtimeOwner
    }

    func launch(model: AppModel, openEditor: @MainActor () -> Void) async {
        openEditor()
        guard !terminated else { return }
        if startTask == nil {
            let start = runtimeOwner.start
            startTask = Task { try await start() }
        }
        guard let startTask else { return }
        do {
            let runtime = try await startTask.value
            guard !terminated else {
                await stopOnce()
                return
            }
            _ = model.installRuntime(runtime)
        } catch {
            guard !terminated else { return }
            model.reportRuntimeLaunchFailure()
        }
    }

    func terminate(model: AppModel) async {
        guard !terminated else { return }
        terminated = true
        await model.deactivate()
        if let startTask {
            _ = try? await startTask.value
        }
        startTask = nil
        await stopOnce()
    }

    private func stopOnce() async {
        guard !stopped else { return }
        stopped = true
        await runtimeOwner.stop()
    }
}

@main
struct MCPasteApp: App {
    @StateObject private var model = AppModel()
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        MenuBarExtra {
            StatusPopoverView(model: model)
        } label: {
            MenuBarLabel(model: model, delegate: delegate)
        }
        .menuBarExtraStyle(.window)

        Window("MCPaste", id: "content") {
            ContentWindowView(model: model)
        }
        .defaultSize(width: 760, height: 520)
    }
}

private struct MenuBarLabel: View {
    @ObservedObject var model: AppModel
    let delegate: AppDelegate
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        BridgeIconView()
            .accessibilityLabel("MCPaste")
            .onAppear {
                delegate.launch(model: model) {
                    ContentWindowOpener.open(
                        openWindow: { openWindow(id: "content") },
                        activate: ContentWindowOpener.activateApp
                    )
                }
            }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    @MainActor private let lifecycle = AppLifecycleController(runtimeOwner: .live())
    @MainActor private var model: AppModel?

    @MainActor
    func launch(model: AppModel, openEditor: @escaping () -> Void) {
        self.model = model
        Task {
            await lifecycle.launch(model: model, openEditor: openEditor)
        }
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        MainActor.assumeIsolated {
            guard let model else { return .terminateNow }
            Task {
                await lifecycle.terminate(model: model)
                NSApplication.shared.reply(toApplicationShouldTerminate: true)
            }
            return .terminateLater
        }
    }
}
