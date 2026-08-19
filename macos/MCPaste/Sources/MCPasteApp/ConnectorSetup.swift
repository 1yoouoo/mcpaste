import Darwin
import Foundation

enum ConnectorSetupError: Error, Equatable {
    case credentialRequired
    case launchFailed
    case processFailed
    case invalidResponse
}

/// Registers the bundled, model-neutral STDIO connector with supported local
/// AI clients after the peer runtime has created its loopback credential.
struct ConnectorSetup {
    private static let maxOutputBytes = 16 * 1024

    let cliURL: URL
    let credentialURL: URL
    let completionTimeout: TimeInterval
    private let configureOutputDescriptor: (Int32) -> Bool

    init(
        cliURL: URL,
        credentialURL: URL,
        completionTimeout: TimeInterval = 2,
        configureOutputDescriptor: @escaping (Int32) -> Bool = ConnectorSetup.setNonblocking
    ) {
        self.cliURL = cliURL
        self.credentialURL = credentialURL
        self.completionTimeout = min(max(completionTimeout, 0.01), 2)
        self.configureOutputDescriptor = configureOutputDescriptor
    }

    static func embeddedCLIURL(bundleURL: URL = Bundle.main.bundleURL) -> URL? {
        let url = bundleURL.appendingPathComponent("Contents/Helpers/mcpaste")
        return FileManager.default.isExecutableFile(atPath: url.path) ? url : nil
    }

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

    func run() async throws -> [String] {
        guard Self.isRegularCredential(credentialURL) else {
            throw ConnectorSetupError.credentialRequired
        }
        let process = Process()
        let stdout = Pipe()
        process.executableURL = cliURL
        process.arguments = ["register"]
        process.standardOutput = stdout
        process.standardError = FileHandle.nullDevice
        do {
            try process.run()
        } catch {
            throw ConnectorSetupError.launchFailed
        }
        let started = DispatchTime.now().uptimeNanoseconds
        let budget = UInt64(completionTimeout * 1_000_000_000)
        let deadline = started &+ budget
        let cleanupBudget = max(1_000_000, budget / 10)
        let workDeadline = deadline - cleanupBudget

        do {
            try? stdout.fileHandleForWriting.close()
            let reader = stdout.fileHandleForReading
            defer { try? reader.close() }
            let descriptor = reader.fileDescriptor
            guard configureOutputDescriptor(descriptor) else {
                throw ConnectorSetupError.processFailed
            }

            let output = try await Self.collectOutput(
                descriptor: descriptor,
                process: process,
                deadline: workDeadline
            )
            return try Self.decodeResponse(output)
        } catch let error as ConnectorSetupError {
            await Self.cleanupOwnedProcess(
                process,
                deadline: min(deadline, DispatchTime.now().uptimeNanoseconds &+ cleanupBudget)
            )
            throw error
        } catch {
            await Self.cleanupOwnedProcess(
                process,
                deadline: min(deadline, DispatchTime.now().uptimeNanoseconds &+ cleanupBudget)
            )
            throw ConnectorSetupError.processFailed
        }
    }

    private static func collectOutput(
        descriptor: Int32,
        process: Process,
        deadline: UInt64
    ) async throws -> Data {
        var output = Data()
        var reachedEOF = false

        while DispatchTime.now().uptimeNanoseconds < deadline {
            if !reachedEOF {
                reachedEOF = try drain(reader: descriptor, into: &output)
            }
            if !process.isRunning {
                guard process.terminationStatus == 0 else {
                    throw ConnectorSetupError.processFailed
                }
                if reachedEOF { return output }
            }
            let now = DispatchTime.now().uptimeNanoseconds
            let remaining = deadline > now ? deadline - now : 0
            if remaining > 0 { try? await Task.sleep(nanoseconds: min(10_000_000, remaining)) }
        }

        if process.isRunning { throw ConnectorSetupError.processFailed }
        guard process.terminationStatus == 0, reachedEOF else {
            throw process.terminationStatus == 0
                ? ConnectorSetupError.invalidResponse
                : ConnectorSetupError.processFailed
        }
        return output
    }

    private static func decodeResponse(_ output: Data) throws -> [String] {
        guard
            output.last == 0x0A,
            output.filter({ $0 == 0x0A }).count == 1,
            !output.contains(0x0D)
        else {
            throw ConnectorSetupError.invalidResponse
        }
        let line = output.dropLast()
        let response: RegistrationResponse
        do {
            response = try JSONDecoder().decode(RegistrationResponse.self, from: line)
        } catch {
            throw ConnectorSetupError.invalidResponse
        }
        switch response.configuredClients {
        case ["Codex"], ["Claude Code"], ["Codex", "Claude Code"]:
            break
        default:
            throw ConnectorSetupError.invalidResponse
        }
        return response.configuredClients
    }

    private static func cleanupOwnedProcess(_ process: Process, deadline: UInt64) async {
        guard process.isRunning else { return }
        process.terminate()

        let now = DispatchTime.now().uptimeNanoseconds
        let killDeadline = now &+ (deadline > now ? deadline - now : 0) / 2
        while process.isRunning && DispatchTime.now().uptimeNanoseconds < killDeadline {
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
        if process.isRunning {
            Darwin.kill(process.processIdentifier, SIGKILL)
        }
        while process.isRunning && DispatchTime.now().uptimeNanoseconds < deadline {
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
        if process.isRunning {
            ConnectorProcessReaper.shared.retainUntilExit(process)
        }
    }

    private static func isRegularCredential(_ url: URL) -> Bool {
        var info = stat()
        return lstat(url.path, &info) == 0 && (info.st_mode & S_IFMT) == S_IFREG
    }

    private static func setNonblocking(_ descriptor: Int32) -> Bool {
        let flags = fcntl(descriptor, F_GETFL)
        return flags >= 0 && fcntl(descriptor, F_SETFL, flags | O_NONBLOCK) == 0
    }

    private static func drain(reader: Int32, into data: inout Data) throws -> Bool {
        var buffer = [UInt8](repeating: 0, count: 4 * 1024)
        while true {
            let count = Darwin.read(reader, &buffer, buffer.count)
            if count > 0 {
                guard data.count <= maxOutputBytes - count else {
                    throw ConnectorSetupError.invalidResponse
                }
                data.append(contentsOf: buffer[0..<count])
                continue
            }
            if count == 0 { return true }
            if errno == EAGAIN || errno == EWOULDBLOCK { return false }
            throw ConnectorSetupError.invalidResponse
        }
    }
}

private final class ConnectorProcessReaper: @unchecked Sendable {
    static let shared = ConnectorProcessReaper()

    private let lock = NSLock()
    private var processes: [ObjectIdentifier: Process] = [:]

    func retainUntilExit(_ process: Process) {
        let identifier = ObjectIdentifier(process)
        lock.lock()
        processes[identifier] = process
        lock.unlock()

        process.terminationHandler = { [weak self] process in
            process.terminationHandler = nil
            self?.release(identifier)
        }
        if !process.isRunning {
            release(identifier)
        }
    }

    private func release(_ identifier: ObjectIdentifier) {
        lock.lock()
        processes.removeValue(forKey: identifier)
        lock.unlock()
    }
}

private struct RegistrationResponse: Decodable {
    let configuredClients: [String]

    enum CodingKeys: String, CodingKey {
        case configuredClients = "configured_clients"
    }
}
