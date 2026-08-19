import Darwin
import Foundation
import XCTest
@testable import MCPasteApp
@testable import MCPasteCore

final class PeerRuntimeProcessTests: XCTestCase {
    private let deviceID = "11111111-1111-4111-8111-111111111111"
    private var directory: URL!
    private var defaults: UserDefaults!
    private var defaultsSuite: String!

    override func setUpWithError() throws {
        directory = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
            .resolvingSymlinksInPath()
            .appendingPathComponent("peer-runtime-process-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defaultsSuite = "PeerRuntimeProcessTests.\(UUID().uuidString)"
        defaults = UserDefaults(suiteName: defaultsSuite)
        defaults.removePersistentDomain(forName: defaultsSuite)
        RuntimeHealthURLProtocol.reset()
    }

    override func tearDownWithError() throws {
        RuntimeHealthURLProtocol.reset()
        defaults.removePersistentDomain(forName: defaultsSuite)
        try? FileManager.default.removeItem(at: directory)
    }

    func testExistingValidCredentialIsReusedWithoutGeneratingRandomBytes() async throws {
        let credential = directory.appendingPathComponent("config/mcpaste/credential.json")
        try writeCredential(credential, token: "existing-token")
        let capture = directory.appendingPathComponent("args.txt")
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: capture),
            credential: credential,
            randomBytes: { _ in XCTFail("Random bytes must not be requested"); return Data() }
        )

        _ = try await process.start()
        await process.stop()

        let object = try credentialObject(at: credential)
        XCTAssertEqual(object["token"] as? String, "existing-token")
    }

    func testCredentialTokenUTF8ByteLimitAccepts4096AndReplaces4097() async throws {
        let cases: [(String, Bool)] = [
            (String(repeating: "x", count: 4 * 1024), true),
            (String(repeating: "é", count: 2 * 1024), true),
            (String(repeating: "x", count: 4 * 1024 + 1), false),
            (String(repeating: "é", count: 2 * 1024 + 1), false)
        ]
        for (token, shouldReuse) in cases {
            let caseDirectory = directory.appendingPathComponent(UUID().uuidString, isDirectory: true)
            let credential = caseDirectory.appendingPathComponent("mcpaste/credential.json")
            try writeCredential(credential, token: token)
            let process = makeRuntime(
                cli: try stdinRuntimeCLI(capture: caseDirectory.appendingPathComponent("args.txt")),
                credential: credential,
                randomBytes: { count in
                    if shouldReuse { XCTFail("Valid boundary token must be reused") }
                    return Data(repeating: 7, count: count)
                }
            )

            _ = try await process.start()
            await process.stop()

            let persisted = try XCTUnwrap(try credentialObject(at: credential)["token"] as? String)
            XCTAssertEqual(persisted == token, shouldReuse)
            XCTAssertLessThanOrEqual(persisted.lengthOfBytes(using: .utf8), 4 * 1024)
        }
    }

    func testMissingAndInvalidCredentialsGenerateInjected32ByteBase64URLToken() async throws {
        for invalidBody in [nil, Data(#"{"endpoint":"https://hosted.invalid","token":"old"}"#.utf8)] {
            let caseDirectory = directory.appendingPathComponent(UUID().uuidString, isDirectory: true)
            let credential = caseDirectory.appendingPathComponent("mcpaste/credential.json")
            if let invalidBody {
                try FileManager.default.createDirectory(at: credential.deletingLastPathComponent(), withIntermediateDirectories: true)
                try invalidBody.write(to: credential)
                try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: credential.path)
            }
            let process = makeRuntime(
                cli: try stdinRuntimeCLI(capture: caseDirectory.appendingPathComponent("args.txt")),
                credential: credential,
                randomBytes: { count in
                    XCTAssertEqual(count, 32)
                    return Data(0..<32)
                }
            )

            _ = try await process.start()
            await process.stop()

            let object = try credentialObject(at: credential)
            XCTAssertEqual(object["endpoint"] as? String, "http://127.0.0.1:38421")
            XCTAssertEqual(object["token"] as? String, "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8")
        }
    }

    func testCredentialWriteUsesSecureModesAtomicReplacementAndNoTemporaryResidue() async throws {
        let parent = directory.appendingPathComponent("open-parent/mcpaste", isDirectory: true)
        try FileManager.default.createDirectory(at: parent, withIntermediateDirectories: true)
        try FileManager.default.setAttributes([.posixPermissions: 0o777], ofItemAtPath: parent.path)
        let credential = parent.appendingPathComponent("credential.json")
        try Data("invalid".utf8).write(to: credential)
        try FileManager.default.setAttributes([.posixPermissions: 0o666], ofItemAtPath: credential.path)
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("args.txt")),
            credential: credential
        )

        _ = try await process.start()
        await process.stop()

        XCTAssertEqual(try permissions(parent), 0o700)
        XCTAssertEqual(try permissions(credential), 0o600)
        let names = try FileManager.default.contentsOfDirectory(atPath: parent.path)
        XCTAssertEqual(names, ["credential.json"])
    }

    func testCredentialSymlinkCannotRedirectGeneratedSecret() async throws {
        let victim = directory.appendingPathComponent("victim.txt")
        try Data("do-not-overwrite".utf8).write(to: victim)
        let parent = directory.appendingPathComponent("mcpaste", isDirectory: true)
        try FileManager.default.createDirectory(at: parent, withIntermediateDirectories: true)
        let credential = parent.appendingPathComponent("credential.json")
        try FileManager.default.createSymbolicLink(at: credential, withDestinationURL: victim)
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("args.txt")),
            credential: credential
        )

        do {
            _ = try await process.start()
            XCTFail("Expected secure credential failure")
        } catch let error as PeerRuntimeProcessError {
            XCTAssertEqual(error, .credentialFailed)
        }

        XCTAssertEqual(try String(contentsOf: victim, encoding: .utf8), "do-not-overwrite")
    }

    func testCredentialReadStaysAnchoredWhenParentPathIsSwapped() async throws {
        let parent = directory.appendingPathComponent("read-parent/mcpaste", isDirectory: true)
        let credential = parent.appendingPathComponent("credential.json")
        try writeCredential(credential, token: "anchored-token")
        let movedParent = directory.appendingPathComponent("read-parent-moved", isDirectory: true)
        let redirectedParent = directory.appendingPathComponent("read-parent-redirected", isDirectory: true)
        try FileManager.default.createDirectory(at: redirectedParent, withIntermediateDirectories: true)
        try writeCredential(redirectedParent.appendingPathComponent("credential.json"), token: "redirected-token")
        let opened = DispatchSemaphore(value: 0)
        let resume = DispatchSemaphore(value: 0)
        let requests = LockedHealthRequests()
        RuntimeHealthURLProtocol.setHandler { request in
            requests.append(request)
            return .json(deviceID: self.deviceID)
        }
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("read-args.txt")),
            credential: credential,
            randomBytes: { _ in XCTFail("Anchored credential must be reused"); return Data() },
            credentialParentOpened: { writing in
                guard !writing else { return }
                opened.signal()
                _ = resume.wait(timeout: .now() + 2)
            }
        )
        let starting = Task { try await process.start() }
        XCTAssertEqual(opened.wait(timeout: .now() + 1), .success)

        try FileManager.default.moveItem(at: parent, to: movedParent)
        try FileManager.default.createSymbolicLink(at: parent, withDestinationURL: redirectedParent)
        resume.signal()

        _ = try await starting.value
        await process.stop()
        XCTAssertEqual(requests.values.first?.value(forHTTPHeaderField: "Authorization"), "Bearer anchored-token")
    }

    func testCredentialWriteStaysAnchoredWhenParentPathIsSwapped() async throws {
        let parent = directory.appendingPathComponent("write-parent/mcpaste", isDirectory: true)
        try FileManager.default.createDirectory(at: parent, withIntermediateDirectories: true)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: parent.path)
        let credential = parent.appendingPathComponent("credential.json")
        let movedParent = directory.appendingPathComponent("write-parent-moved", isDirectory: true)
        let redirectedParent = directory.appendingPathComponent("write-parent-redirected", isDirectory: true)
        try FileManager.default.createDirectory(at: redirectedParent, withIntermediateDirectories: true)
        let opened = DispatchSemaphore(value: 0)
        let resume = DispatchSemaphore(value: 0)
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("write-args.txt")),
            credential: credential,
            credentialParentOpened: { writing in
                guard writing else { return }
                opened.signal()
                _ = resume.wait(timeout: .now() + 2)
            }
        )
        let starting = Task { try await process.start() }
        XCTAssertEqual(opened.wait(timeout: .now() + 1), .success)

        try FileManager.default.moveItem(at: parent, to: movedParent)
        try FileManager.default.createSymbolicLink(at: parent, withDestinationURL: redirectedParent)
        resume.signal()

        _ = try await starting.value
        await process.stop()
        XCTAssertEqual(
            try credentialObject(at: movedParent.appendingPathComponent("credential.json"))["token"] as? String,
            "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
        )
        XCTAssertFalse(FileManager.default.fileExists(
            atPath: redirectedParent.appendingPathComponent("credential.json").path
        ))
    }

    func testCredentialFIFOIsRejectedWithoutBlockingOrReplacement() async throws {
        let parent = directory.appendingPathComponent("fifo-parent/mcpaste", isDirectory: true)
        try FileManager.default.createDirectory(at: parent, withIntermediateDirectories: true)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: parent.path)
        let credential = parent.appendingPathComponent("credential.json")
        XCTAssertEqual(mkfifo(credential.path, 0o600), 0)
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("fifo-args.txt")),
            credential: credential
        )
        let started = Date()

        do {
            _ = try await process.start()
            XCTFail("Expected fixed credential failure")
        } catch let error as PeerRuntimeProcessError {
            XCTAssertEqual(error, .credentialFailed)
        }

        XCTAssertLessThan(Date().timeIntervalSince(started), 0.5)
        var info = stat()
        XCTAssertEqual(lstat(credential.path, &info), 0)
        XCTAssertEqual(info.st_mode & S_IFMT, S_IFIFO)
    }

    func testCredentialPathRejectsSymlinkUnderNonRootParent() async throws {
        let real = directory.appendingPathComponent("real-config", isDirectory: true)
        try FileManager.default.createDirectory(at: real.appendingPathComponent("mcpaste"), withIntermediateDirectories: true)
        let link = directory.appendingPathComponent("config-link", isDirectory: true)
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: real)
        let credential = link.appendingPathComponent("mcpaste/credential.json")
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("symlink-parent-args.txt")),
            credential: credential
        )

        do {
            _ = try await process.start()
            XCTFail("Expected fixed credential failure")
        } catch let error as PeerRuntimeProcessError {
            XCTAssertEqual(error, .credentialFailed)
        }

        XCTAssertFalse(FileManager.default.fileExists(
            atPath: real.appendingPathComponent("mcpaste/credential.json").path
        ))
    }

    func testLaunchUsesPeerArgumentArrayAndKeepsStdinOpenUntilShutdownEOF() async throws {
        let capture = directory.appendingPathComponent("args.txt")
        let eof = directory.appendingPathComponent("eof.txt")
        let cli = try script(
            """
            #!/bin/sh
            printf 'mcpaste-peer-ready\n'
            printf '%s\n' "$@" > '\(capture.path)'
            cat >/dev/null
            echo eof > '\(eof.path)'
            """
        )
        let credential = directory.appendingPathComponent("config/mcpaste/credential.json")
        let process = makeRuntime(cli: cli, credential: credential, displayName: "Test Mac")

        _ = try await process.start()

        XCTAssertFalse(FileManager.default.fileExists(atPath: eof.path))
        let capturedArguments = await waitForFile(capture, timeout: 0.5)
        XCTAssertTrue(capturedArguments)
        let args = try String(contentsOf: capture, encoding: .utf8).split(separator: "\n").map(String.init)
        let deviceID = try XCTUnwrap(argumentValue("--device-id", in: args))
        XCTAssertNotNil(UUID(uuidString: deviceID))
        XCTAssertEqual(args, [
            "peer",
            "--device-id", deviceID,
            "--name", "Test Mac",
            "--credential-file", credential.path,
            "--registry-file", credential.deletingLastPathComponent().appendingPathComponent("peers.json").path,
            "--port", "38421"
        ])
        XCTAssertFalse(args.contains("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"))

        await process.stop()

        let receivedEOF = await waitForFile(eof, timeout: 0.5)
        XCTAssertTrue(receivedEOF)
    }

    func testStartupHealthIsAuthenticatedAndBoundedToTwoSeconds() async throws {
        let requests = LockedHealthRequests()
        RuntimeHealthURLProtocol.setHandler { request in
            requests.append(request)
            return .json(deviceID: self.deviceID)
        }
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("args.txt")),
            credential: directory.appendingPathComponent("config/mcpaste/credential.json"),
            startupTimeout: 2
        )

        _ = try await process.start()
        await process.stop()

        let request = try XCTUnwrap(requests.values.first)
        XCTAssertEqual(request.url?.absoluteString, "http://127.0.0.1:38421/v1/health")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8")
        XCTAssertLessThanOrEqual(request.timeoutInterval, 2)
    }

    func testImmediateExitWithoutReadinessNeverSendsBearerOrReturnsClient() async throws {
        let requests = LockedHealthRequests()
        RuntimeHealthURLProtocol.setHandler { request in
            requests.append(request)
            return .json(deviceID: self.deviceID)
        }
        let process = makeRuntime(
            cli: try script("#!/bin/sh\nexit 0\n"),
            credential: directory.appendingPathComponent("immediate-exit/credential.json"),
            startupTimeout: 0.15,
            shutdownTimeout: 0.15
        )

        await XCTAssertThrowsRuntimeProcessError(.startupFailed) { try await process.start() }

        XCTAssertTrue(requests.values.isEmpty)
    }

    func testReadinessMustBeExactBoundedAndCompleteBeforeHealth() async throws {
        for output in ["wrong-ready\\n", String(repeating: "x", count: 80) + "\\n", ""] {
            RuntimeHealthURLProtocol.reset()
            let requests = LockedHealthRequests()
            RuntimeHealthURLProtocol.setHandler { request in
                requests.append(request)
                return .json(deviceID: self.deviceID)
            }
            let encoded = Data(output.utf8).base64EncodedString()
            let process = makeRuntime(
                cli: try script(
                    "#!/bin/sh\nprintf '%s' '\(encoded)' | /usr/bin/base64 -D\ncat >/dev/null\n"
                ),
                credential: directory.appendingPathComponent("readiness-\(UUID().uuidString)/credential.json"),
                startupTimeout: 0.12,
                shutdownTimeout: 0.12
            )
            let started = Date()

            await XCTAssertThrowsRuntimeProcessError(.startupFailed) { try await process.start() }

            XCTAssertLessThan(Date().timeIntervalSince(started), 0.7)
            XCTAssertTrue(requests.values.isEmpty)
        }
    }

    func testStartupHealthRequiresExpectedDeviceID() async throws {
        RuntimeHealthURLProtocol.setHandler { _ in
            .json(deviceID: "22222222-2222-4222-8222-222222222222")
        }
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("wrong-device-args.txt")),
            credential: directory.appendingPathComponent("wrong-device/credential.json"),
            startupTimeout: 0.12,
            shutdownTimeout: 0.12
        )

        await XCTAssertThrowsRuntimeProcessError(.startupFailed) { try await process.start() }
    }

    func testProxySessionIsRejectedBeforeCredentialGenerationOrLaunch() async throws {
        let launched = directory.appendingPathComponent("proxy-launched.txt")
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [RuntimeHealthURLProtocol.self]
        configuration.connectionProxyDictionary = [
            "HTTPEnable": 1, "HTTPProxy": "127.0.0.2", "HTTPPort": 8080
        ]
        let credential = directory.appendingPathComponent("proxy/credential.json")
        let process = PeerRuntimeProcess(
            cliURL: try script("#!/bin/sh\necho launched > '\(launched.path)'\n"),
            credentialURL: credential,
            defaults: defaults,
            displayName: "Test Mac",
            randomBytes: { _ in XCTFail("Proxy rejection must precede token generation"); return Data() },
            session: URLSession(configuration: configuration),
            startupTimeout: 0.12,
            shutdownTimeout: 0.12
        )

        await XCTAssertThrowsRuntimeProcessError(.startupFailed) { try await process.start() }

        XCTAssertFalse(FileManager.default.fileExists(atPath: credential.path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: launched.path))
    }

    func testHealthRejectsDeclaredAndStreamingBodiesOver4096Bytes() async throws {
        let responses: [HealthStubResponse] = [
            .init(
                statusCode: 200,
                headers: ["Content-Type": "application/json", "Content-Length": "4097"],
                chunks: [Data(#"{"protocol_version":1,"device_id":"11111111-1111-4111-8111-111111111111"}"#.utf8)]
            ),
            .init(
                statusCode: 200,
                headers: ["Content-Type": "application/json"],
                chunks: [Data(repeating: 0x61, count: 4097)]
            )
        ]
        for response in responses {
            RuntimeHealthURLProtocol.reset()
            RuntimeHealthURLProtocol.setHandler { _ in response }
            let process = makeRuntime(
                cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("health-bound-\(UUID().uuidString).txt")),
                credential: directory.appendingPathComponent("health-bound-\(UUID().uuidString)/credential.json"),
                startupTimeout: 0.12,
                shutdownTimeout: 0.12
            )

            await XCTAssertThrowsRuntimeProcessError(.startupFailed) { try await process.start() }
        }
    }

    func testSlowHealthIsCancelledAtSingleStartupDeadline() async throws {
        let requested = DispatchSemaphore(value: 0)
        RuntimeHealthURLProtocol.setHandler { _ in
            requested.signal()
            return .init(
                statusCode: 200,
                headers: ["Content-Type": "application/json"],
                chunks: [Data(#"{"protocol_version":1,"device_id":"11111111-1111-4111-8111-111111111111"}"#.utf8)],
                delay: 2
            )
        }
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("slow-health-args.txt")),
            credential: directory.appendingPathComponent("slow-health/credential.json"),
            startupTimeout: 1,
            shutdownTimeout: 0.12
        )
        let started = Date()
        let starting = Task { try await process.start() }
        XCTAssertEqual(requested.wait(timeout: .now() + 2), .success)

        await XCTAssertThrowsRuntimeProcessError(.startupFailed) { try await starting.value }

        XCTAssertLessThan(Date().timeIntervalSince(started), 1.5)
        let stopDeadline = Date().addingTimeInterval(0.5)
        while RuntimeHealthURLProtocol.stopCount == 0 && Date() < stopDeadline {
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
        XCTAssertGreaterThan(RuntimeHealthURLProtocol.stopCount, 0)
    }

    func testConcurrentStopDuringDelayedHealthCannotReturnClient() async throws {
        let requested = DispatchSemaphore(value: 0)
        let release = DispatchSemaphore(value: 0)
        RuntimeHealthURLProtocol.setHandler { _ in
            requested.signal()
            _ = release.wait(timeout: .now() + 1)
            return .json(deviceID: self.deviceID)
        }
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("concurrent-stop-args.txt")),
            credential: directory.appendingPathComponent("concurrent-stop/credential.json"),
            startupTimeout: 1.5,
            shutdownTimeout: 0.12
        )
        let starting = Task { try await process.start() }
        XCTAssertEqual(requested.wait(timeout: .now() + 2), .success)

        await process.stop()
        release.signal()

        do {
            _ = try await starting.value
            XCTFail("A stopped generation must not return a client")
        } catch let error as PeerRuntimeProcessError {
            XCTAssertEqual(error, .startupFailed)
        }
    }

    func testStartupFailureTerminatesOwnedChildWithinBound() async throws {
        RuntimeHealthURLProtocol.setHandler { _ in throw URLError(.cannotConnectToHost) }
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("args.txt")),
            credential: directory.appendingPathComponent("config/mcpaste/credential.json"),
            startupTimeout: 0.12,
            shutdownTimeout: 0.12
        )
        let start = Date()

        do {
            _ = try await process.start()
            XCTFail("Expected startup failure")
        } catch let error as PeerRuntimeProcessError {
            XCTAssertEqual(error, .startupFailed)
        }

        XCTAssertLessThan(Date().timeIntervalSince(start), 1)
    }

    func testHungChildShutdownIsBoundedAndTerminatesOwnedChild() async throws {
        let pidFile = directory.appendingPathComponent("pid.txt")
        let cli = try script(
            """
            #!/bin/sh
            printf 'mcpaste-peer-ready\n'
            echo $$ > '\(pidFile.path)'
            while :; do sleep 0.05; done
            """
        )
        let process = makeRuntime(
            cli: cli,
            credential: directory.appendingPathComponent("config/mcpaste/credential.json"),
            shutdownTimeout: 0.12
        )
        _ = try await process.start()
        let recordedPID = await waitForFile(pidFile, timeout: 0.5)
        XCTAssertTrue(recordedPID)
        let pid = try XCTUnwrap(Int32(String(contentsOf: pidFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)))
        let start = Date()

        await process.stop()

        XCTAssertLessThan(Date().timeIntervalSince(start), 1)
        let ownedChildExited = await waitForProcessExit(pid, timeout: 0.5)
        XCTAssertTrue(ownedChildExited)
    }

    func testSIGKILLFallbackStopsOwnedTERMImmuneChildWithoutTouchingUnrelatedProcess() async throws {
        let unrelated = Process()
        unrelated.executableURL = URL(fileURLWithPath: "/bin/sleep")
        unrelated.arguments = ["5"]
        try unrelated.run()
        defer { if unrelated.isRunning { unrelated.terminate() } }

        let pidFile = directory.appendingPathComponent("term-immune-pid.txt")
        let cli = try script(
            """
            #!/bin/sh
            printf 'mcpaste-peer-ready\n'
            trap '' TERM
            echo $$ > '\(pidFile.path)'
            while :; do :; done
            """
        )
        let process = makeRuntime(
            cli: cli,
            credential: directory.appendingPathComponent("config/mcpaste/credential.json"),
            shutdownTimeout: 0.12
        )
        _ = try await process.start()
        let recordedPID = await waitForFile(pidFile, timeout: 0.5)
        XCTAssertTrue(recordedPID)
        let pid = try XCTUnwrap(Int32(
            String(contentsOf: pidFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        ))
        let start = Date()

        await process.stop()

        XCTAssertLessThan(Date().timeIntervalSince(start), 1)
        let ownedChildExited = await waitForProcessExit(pid, timeout: 0.5)
        XCTAssertTrue(ownedChildExited)
        XCTAssertTrue(unrelated.isRunning)
    }

    func testDefaultShutdownDeadlineIncludesEOFTermKillAndConfirmedExit() async throws {
        let pidFile = directory.appendingPathComponent("default-timeout-term-immune.txt")
        let cli = try script(
            """
            #!/bin/sh
            printf 'mcpaste-peer-ready\n'
            trap '' TERM
            echo $$ > '\(pidFile.path)'
            while :; do :; done
            """
        )
        let process = makeRuntime(
            cli: cli,
            credential: directory.appendingPathComponent("default-shutdown/credential.json"),
            shutdownTimeout: 2
        )
        _ = try await process.start()
        let recordedPID = await waitForFile(pidFile, timeout: 0.5)
        XCTAssertTrue(recordedPID)
        let pid = try XCTUnwrap(Int32(
            String(contentsOf: pidFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        ))
        let started = Date()

        await process.stop()

        XCTAssertLessThanOrEqual(Date().timeIntervalSince(started), 2.15)
        let exited = await waitForProcessExit(pid, timeout: 0.2)
        XCTAssertTrue(exited)
    }

    func testShutdownNeverTerminatesUnrelatedProcess() async throws {
        let unrelated = Process()
        unrelated.executableURL = URL(fileURLWithPath: "/bin/sleep")
        unrelated.arguments = ["5"]
        try unrelated.run()
        defer { if unrelated.isRunning { unrelated.terminate() } }
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("args.txt")),
            credential: directory.appendingPathComponent("config/mcpaste/credential.json")
        )
        _ = try await process.start()

        await process.stop()

        XCTAssertTrue(unrelated.isRunning)
    }

    func testDefaultsPersistOnlySanitizedDeviceIDAndDisplayNameMetadata() async throws {
        let process = makeRuntime(
            cli: try stdinRuntimeCLI(capture: directory.appendingPathComponent("args.txt")),
            credential: directory.appendingPathComponent("config/mcpaste/credential.json"),
            displayName: "  Desk\nMac\u{0000}  "
        )

        _ = try await process.start()
        await process.stop()

        let domain = try XCTUnwrap(defaults.persistentDomain(forName: defaultsSuite))
        XCTAssertEqual(Set(domain.keys), ["MCPaste.peerDeviceID", "MCPaste.peerDisplayName"])
        XCTAssertNotNil(UUID(uuidString: try XCTUnwrap(domain["MCPaste.peerDeviceID"] as? String)))
        XCTAssertEqual(domain["MCPaste.peerDisplayName"] as? String, "Desk Mac")
        let rendered = String(describing: domain)
        XCTAssertFalse(rendered.contains("AAECAw"))
        XCTAssertFalse(rendered.contains("127.0.0.1"))
        XCTAssertFalse(rendered.contains("credential.json"))
    }

    private func makeRuntime(
        cli: URL,
        credential: URL,
        displayName: String = "Test Mac",
        randomBytes: @escaping @Sendable (Int) throws -> Data = { count in Data(0..<UInt8(count)) },
        credentialParentOpened: @escaping @Sendable (Bool) -> Void = { _ in },
        startupTimeout: TimeInterval = 2,
        shutdownTimeout: TimeInterval = 0.5
    ) -> PeerRuntimeProcess {
        if defaults.string(forKey: "MCPaste.peerDeviceID") == nil {
            defaults.set(deviceID, forKey: "MCPaste.peerDeviceID")
        }
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [RuntimeHealthURLProtocol.self]
        configuration.connectionProxyDictionary = [:]
        RuntimeHealthURLProtocol.setHandlerIfMissing { _ in
            .json(deviceID: self.deviceID)
        }
        return PeerRuntimeProcess(
            cliURL: cli,
            credentialURL: credential,
            defaults: defaults,
            displayName: displayName,
            randomBytes: randomBytes,
            credentialParentOpened: credentialParentOpened,
            session: URLSession(configuration: configuration),
            startupTimeout: startupTimeout,
            shutdownTimeout: shutdownTimeout
        )
    }

    private func stdinRuntimeCLI(capture: URL) throws -> URL {
        try script(
            """
            #!/bin/sh
            printf 'mcpaste-peer-ready\n'
            printf '%s\n' "$@" > '\(capture.path)'
            cat >/dev/null
            """
        )
    }

    private func script(_ contents: String) throws -> URL {
        let url = directory.appendingPathComponent("mcpaste-\(UUID().uuidString)")
        try contents.write(to: url, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
        return url
    }

    private func writeCredential(_ url: URL, token: String) throws {
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data(#"{"endpoint":"http://127.0.0.1:38421","token":"\#(token)"}"#.utf8).write(to: url)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.deletingLastPathComponent().path)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
    }

    private func credentialObject(at url: URL) throws -> [String: Any] {
        try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: url)) as? [String: Any])
    }

    private func permissions(_ url: URL) throws -> Int {
        let attributes = try FileManager.default.attributesOfItem(atPath: url.path)
        return try XCTUnwrap(attributes[.posixPermissions] as? NSNumber).intValue
    }

    private func argumentValue(_ flag: String, in args: [String]) -> String? {
        guard let index = args.firstIndex(of: flag), args.indices.contains(index + 1) else { return nil }
        return args[index + 1]
    }

    private func waitForFile(_ url: URL, timeout: TimeInterval) async -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if FileManager.default.fileExists(atPath: url.path) { return true }
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
        return FileManager.default.fileExists(atPath: url.path)
    }

    private func waitForProcessExit(_ pid: Int32, timeout: TimeInterval) async -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if Darwin.kill(pid, 0) != 0 && errno == ESRCH { return true }
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
        return Darwin.kill(pid, 0) != 0 && errno == ESRCH
    }
}

private struct HealthStubResponse: Sendable {
    var statusCode: Int
    var headers: [String: String]
    var chunks: [Data]
    var responseURL: URL?
    var delay: TimeInterval

    init(
        statusCode: Int,
        headers: [String: String] = [:],
        chunks: [Data] = [],
        responseURL: URL? = nil,
        delay: TimeInterval = 0
    ) {
        self.statusCode = statusCode
        self.headers = headers
        self.chunks = chunks
        self.responseURL = responseURL
        self.delay = delay
    }

    static func json(deviceID: String) -> Self {
        let body = Data(#"{"protocol_version":1,"device_id":"\#(deviceID)"}"#.utf8)
        return .init(
            statusCode: 200,
            headers: ["Content-Type": "application/json", "Content-Length": String(body.count)],
            chunks: [body]
        )
    }
}

private final class RuntimeHealthURLProtocol: URLProtocol, @unchecked Sendable {
    typealias Handler = @Sendable (URLRequest) throws -> HealthStubResponse
    private static let lock = NSLock()
    private static var handler: Handler?
    private static var stops = 0
    private let stateLock = NSLock()
    private var stopped = false

    static func setHandler(_ value: @escaping Handler) {
        lock.lock(); handler = value; lock.unlock()
    }

    static func setHandlerIfMissing(_ value: @escaping Handler) {
        lock.lock()
        if handler == nil { handler = value }
        lock.unlock()
    }

    static func reset() {
        lock.lock(); handler = nil; stops = 0; lock.unlock()
    }

    static var stopCount: Int {
        lock.lock(); defer { lock.unlock() }
        return stops
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.lock.lock(); let handler = Self.handler; Self.lock.unlock()
        DispatchQueue.global().async { [self] in
            do {
                let stub = try XCTUnwrap(handler)(request)
                let deadline = Date().addingTimeInterval(stub.delay)
                while Date() < deadline && !isStopped {
                    Thread.sleep(forTimeInterval: 0.005)
                }
                guard !isStopped else { return }
                let response = HTTPURLResponse(
                    url: stub.responseURL ?? request.url!,
                    statusCode: stub.statusCode,
                    httpVersion: nil,
                    headerFields: stub.headers
                )!
                client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
                for chunk in stub.chunks {
                    guard !isStopped else { return }
                    client?.urlProtocol(self, didLoad: chunk)
                }
                guard !isStopped else { return }
                client?.urlProtocolDidFinishLoading(self)
            } catch {
                guard !isStopped else { return }
                client?.urlProtocol(self, didFailWithError: error)
            }
        }
    }

    override func stopLoading() {
        stateLock.lock()
        let wasStopped = stopped
        stopped = true
        stateLock.unlock()
        guard !wasStopped else { return }
        Self.lock.lock(); Self.stops += 1; Self.lock.unlock()
    }

    private var isStopped: Bool {
        stateLock.lock(); defer { stateLock.unlock() }
        return stopped
    }
}

private final class LockedHealthRequests: @unchecked Sendable {
    private let lock = NSLock()
    private var requests: [URLRequest] = []

    func append(_ request: URLRequest) {
        lock.lock(); requests.append(request); lock.unlock()
    }

    var values: [URLRequest] {
        lock.lock(); defer { lock.unlock() }
        return requests
    }
}

private func XCTAssertThrowsRuntimeProcessError<T>(
    _ expected: PeerRuntimeProcessError,
    operation: () async throws -> T,
    file: StaticString = #filePath,
    line: UInt = #line
) async {
    do {
        _ = try await operation()
        XCTFail("Expected \(expected)", file: file, line: line)
    } catch let error as PeerRuntimeProcessError {
        XCTAssertEqual(error, expected, file: file, line: line)
    } catch {
        XCTFail("Unexpected error type \(type(of: error))", file: file, line: line)
    }
}
