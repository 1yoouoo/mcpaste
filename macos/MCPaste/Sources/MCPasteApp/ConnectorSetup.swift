import Foundation

/// Configures this Mac's own AI-tool connector with the CLI embedded in the app
/// bundle. On the first run it spawns `mcpaste setup`, approves the pairing code
/// the CLI prints using the app's own credential, and lets the CLI store the
/// connector credential and register itself with Codex/Claude Code. On later
/// launches it spawns `mcpaste register`, which only re-registers the executable
/// so AI tools installed after setup are still picked up.
struct ConnectorSetup {
    enum Outcome: Equatable {
        case configured
        case noAITools
        case failed
    }

    let cliURL: URL
    let credentialURL: URL
    let deviceName: String

    static let noAIToolsMarker = "no Codex or Claude Code configuration detected"

    /// The CLI shipped inside the released app bundle; absent in `swift run`
    /// development builds, in which case connector setup is silently skipped.
    /// It lives in Contents/Helpers because Contents/MacOS/mcpaste would
    /// collide with the MCPaste app binary on case-insensitive filesystems.
    static func embeddedCLIURL(bundleURL: URL = Bundle.main.bundleURL) -> URL? {
        let url = bundleURL.appendingPathComponent("Contents/Helpers/mcpaste")
        return FileManager.default.isExecutableFile(atPath: url.path) ? url : nil
    }

    /// Mirrors the CLI's DefaultCredentialPath: $XDG_CONFIG_HOME or ~/.config.
    static func credentialFileURL(
        environment: [String: String] = ProcessInfo.processInfo.environment,
        home: URL = FileManager.default.homeDirectoryForCurrentUser
    ) -> URL {
        let base: URL
        if let configHome = environment["XDG_CONFIG_HOME"], !configHome.isEmpty {
            base = URL(fileURLWithPath: configHome)
        } else {
            base = home.appendingPathComponent(".config")
        }
        return base.appendingPathComponent("mcpaste/credential.json")
    }

    static func shortCode(fromSetupOutput line: String) -> String? {
        for token in line.split(separator: " ") where token.hasPrefix("short_code=") {
            let code = String(token.dropFirst("short_code=".count))
            return code.isEmpty ? nil : code
        }
        return nil
    }

    /// `approve` receives the pairing short code and must approve it through the
    /// app's own API session; setup completes once the CLI claims the approval.
    func run(approve: @escaping (String) async throws -> Void) async -> Outcome {
        if FileManager.default.fileExists(atPath: credentialURL.path) {
            return await runRegister()
        }
        return await runSetup(approve: approve)
    }

    private func runRegister() async -> Outcome {
        let process = Process()
        process.executableURL = cliURL
        process.arguments = ["register"]
        return await finish(process: process)
    }

    private func runSetup(approve: @escaping (String) async throws -> Void) async -> Outcome {
        let process = Process()
        process.executableURL = cliURL
        process.arguments = ["setup", "--name", "\(deviceName) AI tools"]
        let stdout = Pipe()
        process.standardOutput = stdout
        let stderr = Pipe()
        process.standardError = stderr

        do {
            try process.run()
        } catch {
            return .failed
        }

        let firstLine = await Self.firstLine(from: stdout.fileHandleForReading)
        guard let code = Self.shortCode(fromSetupOutput: firstLine) else {
            process.terminate()
            return .failed
        }
        do {
            try await approve(code)
        } catch {
            // Without an approval the CLI would poll for five minutes; stop it.
            process.terminate()
            return .failed
        }
        return await Self.outcome(awaiting: process, stderr: stderr)
    }

    private func finish(process: Process) async -> Outcome {
        let stderr = Pipe()
        process.standardError = stderr
        process.standardOutput = Pipe()
        do {
            try process.run()
        } catch {
            return .failed
        }
        return await Self.outcome(awaiting: process, stderr: stderr)
    }

    private static func outcome(awaiting process: Process, stderr: Pipe) async -> Outcome {
        let errorOutput = await withCheckedContinuation { continuation in
            DispatchQueue.global(qos: .utility).async {
                let data = stderr.fileHandleForReading.readDataToEndOfFile()
                process.waitUntilExit()
                continuation.resume(returning: String(decoding: data, as: UTF8.self))
            }
        }
        if process.terminationStatus == 0 { return .configured }
        return errorOutput.contains(noAIToolsMarker) ? .noAITools : .failed
    }

    // The readability handler runs serially and is removed before the
    // continuation resumes, so the box needs no locking.
    private final class LineBuffer: @unchecked Sendable { var data = Data() }

    private static func firstLine(from handle: FileHandle) async -> String {
        let buffer = LineBuffer()
        return await withCheckedContinuation { continuation in
            handle.readabilityHandler = { handle in
                let chunk = handle.availableData
                if chunk.isEmpty {
                    handle.readabilityHandler = nil
                    continuation.resume(returning: String(decoding: buffer.data, as: UTF8.self))
                    return
                }
                buffer.data.append(chunk)
                if let newline = buffer.data.firstIndex(of: 0x0A) {
                    handle.readabilityHandler = nil
                    continuation.resume(returning: String(decoding: buffer.data[..<newline], as: UTF8.self))
                }
            }
        }
    }
}
