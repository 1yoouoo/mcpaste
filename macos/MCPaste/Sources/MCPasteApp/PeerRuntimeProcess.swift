import Darwin
import Foundation
import MCPasteCore
import Security

enum PeerRuntimeProcessError: Error, Equatable {
    case credentialFailed
    case launchFailed
    case startupFailed
}

actor PeerRuntimeProcess {
    typealias RandomBytes = @Sendable (Int) throws -> Data

    private enum Lifecycle: Equatable {
        case idle
        case starting
        case running
        case stopping
    }

    private static let endpoint = "http://127.0.0.1:38421"
    private static let deviceIDKey = "MCPaste.peerDeviceID"
    private static let displayNameKey = "MCPaste.peerDisplayName"

    private let cliURL: URL
    private let credentialURL: URL
    private let defaults: UserDefaults
    private let requestedDisplayName: String
    private let randomBytes: RandomBytes
    private let credentialParentOpened: @Sendable (Bool) -> Void
    private let session: URLSession
    private let startupTimeout: TimeInterval
    private let shutdownTimeout: TimeInterval

    private var lifecycle = Lifecycle.idle
    private var generation: UInt64 = 0
    private var child: Process?
    private var stdinWriter: FileHandle?
    private var readinessReader: FileHandle?

    init(
        cliURL: URL,
        credentialURL: URL = ConnectorSetup.credentialFileURL(),
        defaults: UserDefaults = .standard,
        displayName: String = Host.current().localizedName ?? "Mac",
        randomBytes: @escaping RandomBytes = { try PeerRuntimeProcess.secureRandomBytes(count: $0) },
        credentialParentOpened: @escaping @Sendable (Bool) -> Void = { _ in },
        session: URLSession = PeerRuntimeProcess.localSession(),
        startupTimeout: TimeInterval = 2,
        shutdownTimeout: TimeInterval = 2
    ) {
        self.cliURL = cliURL
        self.credentialURL = credentialURL
        self.defaults = defaults
        self.requestedDisplayName = displayName
        self.randomBytes = randomBytes
        self.credentialParentOpened = credentialParentOpened
        self.session = session
        self.startupTimeout = min(max(startupTimeout, 0.01), 2)
        self.shutdownTimeout = min(max(shutdownTimeout, 0.01), 2)
    }

    func start() async throws -> PeerRuntimeClient {
        guard lifecycle == .idle, child == nil else { throw PeerRuntimeProcessError.launchFailed }
        guard let proxy = session.configuration.connectionProxyDictionary, proxy.isEmpty else {
            throw PeerRuntimeProcessError.startupFailed
        }
        let credential = try prepareCredential()
        let metadata = prepareMetadata()
        let process = Process()
        let stdin = Pipe()
        let stdout = Pipe()
        process.executableURL = cliURL
        process.arguments = [
            "peer",
            "--device-id", metadata.deviceID,
            "--name", metadata.displayName,
            "--credential-file", credentialURL.path,
            "--registry-file", credentialURL.deletingLastPathComponent().appendingPathComponent("peers.json").path,
            "--port", "38421"
        ]
        process.standardInput = stdin
        process.standardOutput = stdout
        process.standardError = FileHandle.nullDevice
        do {
            try process.run()
        } catch {
            try? stdin.fileHandleForWriting.close()
            try? stdin.fileHandleForReading.close()
            try? stdout.fileHandleForWriting.close()
            try? stdout.fileHandleForReading.close()
            throw PeerRuntimeProcessError.launchFailed
        }
        try? stdin.fileHandleForReading.close()
        try? stdout.fileHandleForWriting.close()
        generation &+= 1
        let launchedGeneration = generation
        lifecycle = .starting
        child = process
        stdinWriter = stdin.fileHandleForWriting
        readinessReader = stdout.fileHandleForReading
        let deadline = Self.deadline(after: startupTimeout)

        do {
            try await waitForReadiness(
                process: process,
                generation: launchedGeneration,
                deadline: deadline
            )
            try await waitForHealth(
                token: credential.token,
                deviceID: metadata.deviceID,
                process: process,
                generation: launchedGeneration,
                deadline: deadline
            )
            guard owns(process, generation: launchedGeneration, lifecycle: .starting), process.isRunning else {
                throw PeerRuntimeProcessError.startupFailed
            }
            let client = try PeerRuntimeClient(
                baseURL: URL(string: Self.endpoint)!,
                token: credential.token,
                session: session
            )
            lifecycle = .running
            return client
        } catch {
            await stop(process: process, generation: launchedGeneration)
            throw PeerRuntimeProcessError.startupFailed
        }
    }

    func stop() async {
        guard let process = child else {
            lifecycle = .idle
            return
        }
        await stop(process: process, generation: generation)
    }

    private func stop(process: Process, generation stoppingGeneration: UInt64) async {
        guard owns(process, generation: stoppingGeneration) else { return }
        guard lifecycle != .stopping else { return }
        lifecycle = .stopping
        try? stdinWriter?.close()
        stdinWriter = nil
        try? readinessReader?.close()
        readinessReader = nil

        let started = DispatchTime.now().uptimeNanoseconds
        let budget = Self.nanoseconds(shutdownTimeout)
        let deadline = started &+ budget
        let eofDeadline = started &+ budget / 3
        let termDeadline = started &+ (budget * 2) / 3

        if !(await waitForExit(process, deadline: eofDeadline)), owns(process, generation: stoppingGeneration), process.isRunning {
            process.terminate()
            if !(await waitForExit(process, deadline: termDeadline)), owns(process, generation: stoppingGeneration), process.isRunning {
                Darwin.kill(process.processIdentifier, SIGKILL)
                _ = await waitForExit(process, deadline: deadline)
            }
        }
        if !process.isRunning {
            clearOwnership(process, generation: stoppingGeneration)
        } else if owns(process, generation: stoppingGeneration) {
            Task { await self.reap(process: process, generation: stoppingGeneration) }
        }
    }

    private func waitForReadiness(
        process: Process,
        generation expectedGeneration: UInt64,
        deadline: UInt64
    ) async throws {
        guard let reader = readinessReader else { throw PeerRuntimeProcessError.startupFailed }
        let descriptor = reader.fileDescriptor
        let flags = fcntl(descriptor, F_GETFL)
        guard flags >= 0, fcntl(descriptor, F_SETFL, flags | O_NONBLOCK) == 0 else {
            throw PeerRuntimeProcessError.startupFailed
        }
        let expected = Data("mcpaste-peer-ready\n".utf8)
        var received = Data()
        var buffer = [UInt8](repeating: 0, count: 64)
        while DispatchTime.now().uptimeNanoseconds < deadline {
            guard owns(process, generation: expectedGeneration, lifecycle: .starting), process.isRunning else {
                throw PeerRuntimeProcessError.startupFailed
            }
            let count = Darwin.read(descriptor, &buffer, buffer.count)
            if count > 0 {
                received.append(contentsOf: buffer[0..<count])
                guard received.count <= expected.count, expected.starts(with: received) else {
                    throw PeerRuntimeProcessError.startupFailed
                }
                if received == expected {
                    guard process.isRunning else { throw PeerRuntimeProcessError.startupFailed }
                    try? reader.close()
                    if owns(process, generation: expectedGeneration) { readinessReader = nil }
                    return
                }
                continue
            }
            if count == 0 { throw PeerRuntimeProcessError.startupFailed }
            guard errno == EAGAIN || errno == EWOULDBLOCK else {
                throw PeerRuntimeProcessError.startupFailed
            }
            try await Self.pause(until: deadline, maximum: 0.01)
        }
        throw PeerRuntimeProcessError.startupFailed
    }

    private func waitForHealth(
        token: String,
        deviceID: String,
        process: Process,
        generation expectedGeneration: UInt64,
        deadline: UInt64
    ) async throws {
        while DispatchTime.now().uptimeNanoseconds < deadline {
            guard owns(process, generation: expectedGeneration, lifecycle: .starting), process.isRunning else {
                throw PeerRuntimeProcessError.startupFailed
            }
            let remaining = max(0.01, Self.remainingSeconds(until: deadline))
            var request = URLRequest(
                url: URL(string: Self.endpoint + "/v1/health")!,
                cachePolicy: .reloadIgnoringLocalCacheData,
                timeoutInterval: min(remaining, 2)
            )
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
            do {
                let loaded = try await loadHealth(request, deadline: deadline)
                if
                    loaded.response.statusCode == 200,
                    let object = try? JSONSerialization.jsonObject(with: loaded.data) as? [String: Any],
                    object["protocol_version"] as? Int == 1,
                    object["device_id"] as? String == deviceID,
                    owns(process, generation: expectedGeneration, lifecycle: .starting),
                    process.isRunning
                {
                    return
                }
            } catch {}
            try await Self.pause(until: deadline, maximum: 0.05)
        }
        throw PeerRuntimeProcessError.startupFailed
    }

    private func loadHealth(_ request: URLRequest, deadline: UInt64) async throws -> HealthLoad {
        try await HealthRequest(
            configuration: session.configuration,
            request: request,
            deadline: deadline
        ).load()
    }

    private func waitForExit(_ process: Process, deadline: UInt64) async -> Bool {
        while process.isRunning && DispatchTime.now().uptimeNanoseconds < deadline {
            try? await Self.pause(until: deadline, maximum: 0.01)
        }
        return !process.isRunning
    }

    private func reap(process: Process, generation reapingGeneration: UInt64) async {
        while owns(process, generation: reapingGeneration), process.isRunning {
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
        if !process.isRunning { clearOwnership(process, generation: reapingGeneration) }
    }

    private func owns(
        _ process: Process,
        generation expectedGeneration: UInt64,
        lifecycle expectedLifecycle: Lifecycle? = nil
    ) -> Bool {
        guard generation == expectedGeneration, child === process else { return false }
        return expectedLifecycle == nil || lifecycle == expectedLifecycle
    }

    private func clearOwnership(_ process: Process, generation expectedGeneration: UInt64) {
        guard owns(process, generation: expectedGeneration), !process.isRunning else { return }
        child = nil
        stdinWriter = nil
        readinessReader = nil
        lifecycle = .idle
    }

    private static func nanoseconds(_ interval: TimeInterval) -> UInt64 {
        UInt64(max(0.001, min(interval, 2)) * 1_000_000_000)
    }

    private static func deadline(after interval: TimeInterval) -> UInt64 {
        DispatchTime.now().uptimeNanoseconds &+ nanoseconds(interval)
    }

    private static func remainingSeconds(until deadline: UInt64) -> TimeInterval {
        let now = DispatchTime.now().uptimeNanoseconds
        guard deadline > now else { return 0 }
        return TimeInterval(deadline - now) / 1_000_000_000
    }

    private static func pause(until deadline: UInt64, maximum: TimeInterval) async throws {
        let seconds = min(maximum, remainingSeconds(until: deadline))
        guard seconds > 0 else { return }
        try await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
    }

    private func prepareCredential() throws -> RuntimeCredential {
        if let existing = loadCredential() { return existing }
        guard
            let data = try? randomBytes(32),
            data.count == 32
        else {
            throw PeerRuntimeProcessError.credentialFailed
        }
        let token = data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
        let credential = RuntimeCredential(endpoint: Self.endpoint, token: token)
        try writeCredential(credential)
        return credential
    }

    private func loadCredential() -> RuntimeCredential? {
        guard let (directoryFD, name) = try? openCredentialParent(create: false) else { return nil }
        defer { Darwin.close(directoryFD) }
        credentialParentOpened(false)
        let descriptor = openat(
            directoryFD,
            name,
            O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC
        )
        guard descriptor >= 0 else { return nil }
        var info = stat()
        guard
            fstat(descriptor, &info) == 0,
            (info.st_mode & S_IFMT) == S_IFREG,
            (info.st_mode & 0o077) == 0,
            info.st_size >= 0,
            info.st_size <= 16 * 1024
        else {
            Darwin.close(descriptor)
            return nil
        }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4 * 1024)
        var readSucceeded = true
        while data.count <= 16 * 1024 {
            let count = Darwin.read(descriptor, &buffer, buffer.count)
            if count == 0 { break }
            if count < 0 {
                readSucceeded = false
                break
            }
            data.append(contentsOf: buffer[0..<count])
        }
        let closeSucceeded = Darwin.close(descriptor) == 0
        guard
            readSucceeded,
            closeSucceeded,
            data.count <= 16 * 1024,
            let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
            Set(object.keys) == ["endpoint", "token"],
            let endpoint = object["endpoint"] as? String,
            let token = object["token"] as? String,
            validCredential(endpoint: endpoint, token: token)
        else {
            return nil
        }
        return RuntimeCredential(endpoint: endpoint, token: token)
    }

    private func validCredential(endpoint: String, token: String) -> Bool {
        endpoint == Self.endpoint && !token.isEmpty && token.lengthOfBytes(using: .utf8) <= 4 * 1024 && token.unicodeScalars.allSatisfy {
            !$0.properties.isWhitespace && !CharacterSet.controlCharacters.contains($0)
        }
    }

    private func writeCredential(_ credential: RuntimeCredential) throws {
        do {
            let (directoryFD, name) = try openCredentialParent(create: true)
            defer { Darwin.close(directoryFD) }
            credentialParentOpened(true)
            var target = stat()
            if fstatat(directoryFD, name, &target, AT_SYMLINK_NOFOLLOW) == 0 {
                guard (target.st_mode & S_IFMT) == S_IFREG else {
                    throw PeerRuntimeProcessError.credentialFailed
                }
            } else if errno != ENOENT {
                throw PeerRuntimeProcessError.credentialFailed
            }
            var data = try JSONEncoder().encode(credential)
            data.append(0x0A)
            let temporary = ".credential-\(UUID().uuidString.lowercased())"
            let descriptor = openat(
                directoryFD,
                temporary,
                O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC,
                S_IRUSR | S_IWUSR
            )
            guard descriptor >= 0 else { throw PeerRuntimeProcessError.credentialFailed }
            var completed = false
            var descriptorOpen = true
            defer {
                if descriptorOpen { Darwin.close(descriptor) }
                if !completed { _ = unlinkat(directoryFD, temporary, 0) }
            }
            let writeResult = data.withUnsafeBytes { buffer -> Bool in
                guard let base = buffer.baseAddress else { return data.isEmpty }
                var offset = 0
                while offset < buffer.count {
                    let count = Darwin.write(descriptor, base.advanced(by: offset), buffer.count - offset)
                    if count <= 0 { return false }
                    offset += count
                }
                return true
            }
            guard writeResult, fsync(descriptor) == 0, fchmod(descriptor, S_IRUSR | S_IWUSR) == 0 else {
                throw PeerRuntimeProcessError.credentialFailed
            }
            guard Darwin.close(descriptor) == 0 else {
                descriptorOpen = false
                throw PeerRuntimeProcessError.credentialFailed
            }
            descriptorOpen = false
            guard renameat(directoryFD, temporary, directoryFD, name) == 0 else {
                throw PeerRuntimeProcessError.credentialFailed
            }
            completed = true
            guard fsync(directoryFD) == 0 else { throw PeerRuntimeProcessError.credentialFailed }
        } catch let error as PeerRuntimeProcessError {
            throw error
        } catch {
            throw PeerRuntimeProcessError.credentialFailed
        }
    }

    private func openCredentialParent(create: Bool) throws -> (Int32, String) {
        let path = credentialURL.standardizedFileURL.path
        let name = (path as NSString).lastPathComponent
        guard path.first == "/", !name.isEmpty, name != ".", name != ".." else {
            throw PeerRuntimeProcessError.credentialFailed
        }
        var descriptor = open("/", O_RDONLY | O_DIRECTORY | O_CLOEXEC)
        guard descriptor >= 0 else { throw PeerRuntimeProcessError.credentialFailed }
        var keepDescriptor = false
        defer { if !keepDescriptor { Darwin.close(descriptor) } }
        var components = credentialURL.deletingLastPathComponent().standardizedFileURL.pathComponents
            .filter { $0 != "/" }
        var symlinkCount = 0
        while !components.isEmpty {
            let component = components.removeFirst()
            var next = openat(descriptor, component, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
            let openError = errno
            if next < 0 && Self.isSymlink(at: descriptor, name: component) {
                guard symlinkCount < 8, Self.isTrustedSystemDirectory(descriptor) else {
                    throw PeerRuntimeProcessError.credentialFailed
                }
                let target = try Self.readSymlink(at: descriptor, name: component)
                symlinkCount += 1
                if target.first == "/" {
                    guard Darwin.close(descriptor) == 0 else {
                        throw PeerRuntimeProcessError.credentialFailed
                    }
                    descriptor = open("/", O_RDONLY | O_DIRECTORY | O_CLOEXEC)
                    guard descriptor >= 0 else { throw PeerRuntimeProcessError.credentialFailed }
                }
                components = target.split(separator: "/", omittingEmptySubsequences: true).map(String.init) + components
                continue
            }
            if next < 0 && openError == ENOENT && create {
                guard mkdirat(descriptor, component, S_IRWXU) == 0 || errno == EEXIST else {
                    throw PeerRuntimeProcessError.credentialFailed
                }
                next = openat(descriptor, component, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
            }
            guard next >= 0 else { throw PeerRuntimeProcessError.credentialFailed }
            guard Darwin.close(descriptor) == 0 else {
                Darwin.close(next)
                throw PeerRuntimeProcessError.credentialFailed
            }
            descriptor = next
        }
        if create {
            guard fchmod(descriptor, S_IRWXU) == 0 else {
                throw PeerRuntimeProcessError.credentialFailed
            }
        }
        var info = stat()
        guard
            fstat(descriptor, &info) == 0,
            (info.st_mode & S_IFMT) == S_IFDIR,
            (info.st_mode & 0o077) == 0
        else {
            throw PeerRuntimeProcessError.credentialFailed
        }
        keepDescriptor = true
        return (descriptor, name)
    }

    private static func isSymlink(at directoryFD: Int32, name: String) -> Bool {
        var info = stat()
        return fstatat(directoryFD, name, &info, AT_SYMLINK_NOFOLLOW) == 0 &&
            (info.st_mode & S_IFMT) == S_IFLNK
    }

    private static func isTrustedSystemDirectory(_ descriptor: Int32) -> Bool {
        var info = stat()
        return fstat(descriptor, &info) == 0 && info.st_uid == 0 && (info.st_mode & 0o022) == 0
    }

    private static func readSymlink(at directoryFD: Int32, name: String) throws -> String {
        var buffer = [UInt8](repeating: 0, count: 4 * 1024)
        let count = buffer.withUnsafeMutableBytes { bytes in
            readlinkat(
                directoryFD,
                name,
                bytes.baseAddress!.assumingMemoryBound(to: CChar.self),
                bytes.count
            )
        }
        guard count > 0, count < buffer.count, let value = String(bytes: buffer[0..<count], encoding: .utf8) else {
            throw PeerRuntimeProcessError.credentialFailed
        }
        return value
    }

    private func prepareMetadata() -> (deviceID: String, displayName: String) {
        let deviceID: String
        if let saved = defaults.string(forKey: Self.deviceIDKey), let uuid = UUID(uuidString: saved) {
            deviceID = uuid.uuidString.lowercased()
        } else {
            deviceID = UUID().uuidString.lowercased()
        }
        let displayName = Self.sanitizeDisplayName(requestedDisplayName)
        defaults.set(deviceID, forKey: Self.deviceIDKey)
        defaults.set(displayName, forKey: Self.displayNameKey)
        return (deviceID, displayName)
    }

    private static func sanitizeDisplayName(_ value: String) -> String {
        let mapped = String(value.unicodeScalars.map { scalar in
            CharacterSet.controlCharacters.contains(scalar) || scalar.properties.isWhitespace ? " " : Character(String(scalar))
        })
        let collapsed = mapped.split(whereSeparator: { $0.isWhitespace }).joined(separator: " ")
        let result = String(collapsed.prefix(80))
        return result.isEmpty ? "Mac" : result
    }

    private static func secureRandomBytes(count: Int) throws -> Data {
        var data = Data(count: count)
        let status = data.withUnsafeMutableBytes { buffer in
            SecRandomCopyBytes(kSecRandomDefault, count, buffer.baseAddress!)
        }
        guard status == errSecSuccess else { throw PeerRuntimeProcessError.credentialFailed }
        return data
    }

    private static func localSession() -> URLSession {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.connectionProxyDictionary = [:]
        configuration.urlCache = nil
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        return URLSession(configuration: configuration)
    }
}

private struct RuntimeCredential: Codable {
    let endpoint: String
    let token: String
}

private struct HealthLoad: @unchecked Sendable {
    let response: HTTPURLResponse
    let data: Data
}

private final class HealthRequest: NSObject, URLSessionDataDelegate, @unchecked Sendable {
    private let configuration: URLSessionConfiguration
    private let request: URLRequest
    private let deadline: UInt64
    private let lock = NSLock()
    private var continuation: CheckedContinuation<HealthLoad, Error>?
    private var session: URLSession?
    private var task: URLSessionDataTask?
    private var timeoutTask: Task<Void, Never>?
    private var response: HTTPURLResponse?
    private var data = Data()

    init(configuration: URLSessionConfiguration, request: URLRequest, deadline: UInt64) {
        self.configuration = configuration
        self.request = request
        self.deadline = deadline
    }

    func load() async throws -> HealthLoad {
        try await withTaskCancellationHandler(operation: {
            try await withCheckedThrowingContinuation { continuation in
                lock.lock()
                self.continuation = continuation
                let session = URLSession(configuration: configuration, delegate: self, delegateQueue: nil)
                let task = session.dataTask(with: request)
                self.session = session
                self.task = task
                timeoutTask = Task { [weak self] in
                    guard let self else { return }
                    let now = DispatchTime.now().uptimeNanoseconds
                    if deadline > now {
                        try? await Task.sleep(nanoseconds: deadline - now)
                    }
                    guard !Task.isCancelled else { return }
                    self.cancel()
                }
                lock.unlock()
                task.resume()
            }
        }, onCancel: { self.cancel() })
    }

    func urlSession(
        _ session: URLSession,
        dataTask: URLSessionDataTask,
        didReceive response: URLResponse,
        completionHandler: @escaping (URLSession.ResponseDisposition) -> Void
    ) {
        guard
            let response = response as? HTTPURLResponse,
            response.url == request.url,
            response.expectedContentLength <= 4 * 1024
        else {
            completionHandler(.cancel)
            finish(.failure(PeerRuntimeProcessError.startupFailed))
            return
        }
        lock.lock()
        self.response = response
        lock.unlock()
        completionHandler(.allow)
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive bytes: Data) {
        lock.lock()
        guard continuation != nil, data.count <= 4 * 1024 - bytes.count else {
            lock.unlock()
            finish(.failure(PeerRuntimeProcessError.startupFailed))
            return
        }
        data.append(bytes)
        lock.unlock()
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        if error != nil {
            finish(.failure(PeerRuntimeProcessError.startupFailed))
            return
        }
        lock.lock()
        let response = self.response
        let data = self.data
        lock.unlock()
        guard let response else {
            finish(.failure(PeerRuntimeProcessError.startupFailed))
            return
        }
        finish(.success(HealthLoad(response: response, data: data)))
    }

    func urlSession(
        _ session: URLSession,
        task: URLSessionTask,
        willPerformHTTPRedirection response: HTTPURLResponse,
        newRequest request: URLRequest,
        completionHandler: @escaping (URLRequest?) -> Void
    ) {
        completionHandler(nil)
    }

    private func cancel() {
        finish(.failure(PeerRuntimeProcessError.startupFailed))
    }

    private func finish(_ result: Result<HealthLoad, Error>) {
        lock.lock()
        guard let continuation else {
            lock.unlock()
            return
        }
        self.continuation = nil
        let timeoutTask = self.timeoutTask
        self.timeoutTask = nil
        let task = self.task
        let session = self.session
        self.task = nil
        self.session = nil
        lock.unlock()

        timeoutTask?.cancel()
        switch result {
        case .success(let loaded):
            session?.finishTasksAndInvalidate()
            continuation.resume(returning: loaded)
        case .failure(let error):
            task?.cancel()
            session?.invalidateAndCancel()
            continuation.resume(throwing: error)
        }
    }
}
