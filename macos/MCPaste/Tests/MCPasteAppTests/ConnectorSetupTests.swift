import Darwin
import Foundation
import XCTest
@testable import MCPasteApp

final class ConnectorSetupTests: XCTestCase {
    private var directory: URL!

    override func setUpWithError() throws {
        directory = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
            .resolvingSymlinksInPath()
            .appendingPathComponent("connector-setup-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: directory)
    }

    func testCredentialFileURLPrefersXDGConfigHome() {
        let url = ConnectorSetup.credentialFileURL(
            environment: ["XDG_CONFIG_HOME": "/custom/config"],
            home: URL(fileURLWithPath: "/Users/example")
        )
        XCTAssertEqual(url.path, "/custom/config/mcpaste/credential.json")
    }

    func testCredentialFileURLDefaultsToHomeConfig() {
        let url = ConnectorSetup.credentialFileURL(
            environment: [:],
            home: URL(fileURLWithPath: "/Users/example")
        )
        XCTAssertEqual(url.path, "/Users/example/.config/mcpaste/credential.json")
    }

    func testRunLaunchesOnlyRegisterAndDecodesExactOneLineResponse() async throws {
        let arguments = directory.appendingPathComponent("arguments.txt")
        let cli = try stubCLI(
            """
            #!/bin/sh
            printf '%s\n' "$@" > '\(arguments.path)'
            printf '{"configured_clients":["Codex","Claude Code"]}\n'
            """
        )
        let setup = ConnectorSetup(cliURL: cli, credentialURL: try existingCredential())

        let names = try await setup.run()

        XCTAssertEqual(names, ["Codex", "Claude Code"])
        XCTAssertEqual(try String(contentsOf: arguments, encoding: .utf8), "register\n")
    }

    func testRunAllowsRegisterWithinProductionCompletionTimeout() async throws {
        let cli = try stubCLI(
            """
            #!/bin/sh
            /bin/sleep 0.3
            printf '{"configured_clients":["Codex"]}\n'
            """
        )
        let setup = ConnectorSetup(
            cliURL: cli,
            credentialURL: try existingCredential(),
            completionTimeout: 2
        )

        let names = try await setup.run()

        XCTAssertEqual(names, ["Codex"])
    }

    func testRunAcceptsEachCanonicalConfiguredClientList() async throws {
        for clients in [
            #"["Codex"]"#,
            #"["Claude Code"]"#,
            #"["Codex","Claude Code"]"#
        ] {
            let output = "{\"configured_clients\":\(clients)}\n"
            let setup = ConnectorSetup(cliURL: try outputCLI(output), credentialURL: try existingCredential())
            _ = try await setup.run()
        }
    }

    func testRunRequiresCredentialBeforeLaunchingCLI() async throws {
        let launched = directory.appendingPathComponent("launched.txt")
        let cli = try stubCLI(
            """
            #!/bin/sh
            echo launched > '\(launched.path)'
            printf '{"configured_clients":["Codex"]}\n'
            """
        )
        let missing = directory.appendingPathComponent("missing/credential.json")
        let setup = ConnectorSetup(cliURL: cli, credentialURL: missing)

        await XCTAssertThrowsConnectorError(.credentialRequired) { try await setup.run() }

        XCTAssertFalse(FileManager.default.fileExists(atPath: launched.path))
    }

    func testRunMapsLaunchFailureToFixedError() async {
        let setup = ConnectorSetup(
            cliURL: directory.appendingPathComponent("missing-mcpaste"),
            credentialURL: try! existingCredential()
        )

        await XCTAssertThrowsConnectorError(.launchFailed) { try await setup.run() }
    }

    func testRunRejectsNonzeroExitWithoutLeakingStderrOrPaths() async throws {
        let secret = "stderr-secret"
        let cli = try stubCLI(
            """
            #!/bin/sh
            echo '\(secret)' >&2
            exit 7
            """
        )
        let setup = ConnectorSetup(cliURL: cli, credentialURL: try existingCredential())

        do {
            _ = try await setup.run()
            XCTFail("Expected nonzero exit error")
        } catch let error as ConnectorSetupError {
            XCTAssertEqual(error, .processFailed)
            XCTAssertFalse(String(describing: error).contains(secret))
            XCTAssertFalse(String(describing: error).contains(cli.path))
        }
    }

    func testRunRejectsMalformedMissingNewlineAndMultilineOutput() async throws {
        let outputs = [
            "not-json\\n",
            #"{"configured_clients":["Codex"]}"#,
            """
            {"configured_clients":["Codex"]}
            trailing
            """
        ]
        for output in outputs {
            let cli = try outputCLI(output)
            let setup = ConnectorSetup(cliURL: cli, credentialURL: try existingCredential())
            await XCTAssertThrowsConnectorError(.invalidResponse) { try await setup.run() }
        }
    }

    func testRunRejectsNoncanonicalConfiguredClientLists() async throws {
        for json in [
            #"{"configured_clients":[]}"#,
            #"{"configured_clients":["Claude Code","Codex"]}"#,
            #"{"configured_clients":["Codex","Codex"]}"#,
            #"{"configured_clients":["Other Client"]}"#,
            #"{"configured_clients":"Codex"}"#
        ] {
            let setup = ConnectorSetup(cliURL: try outputCLI(json + "\n"), credentialURL: try existingCredential())
            await XCTAssertThrowsConnectorError(.invalidResponse) { try await setup.run() }
        }
    }

    func testRunRejectsOversizedOutputWhileStreaming() async throws {
        let cli = try stubCLI(
            """
            #!/bin/sh
            i=0
            while [ $i -lt 20000 ]; do printf x; i=$((i + 1)); done
            printf '\n'
            """
        )
        let setup = ConnectorSetup(cliURL: cli, credentialURL: try existingCredential(), completionTimeout: 1)

        await XCTAssertThrowsConnectorError(.invalidResponse) { try await setup.run() }
    }

    func testRunCleansUpOwnedChildWhenOutputDescriptorSetupFails() async throws {
        let pidFile = directory.appendingPathComponent("descriptor-failure-pid.txt")
        let cli = try stubCLI(
            """
            #!/bin/sh
            trap '' TERM
            echo $$ > '\(pidFile.path)'
            while :; do sleep 0.05; done
            """
        )
        let setup = ConnectorSetup(
            cliURL: cli,
            credentialURL: try existingCredential(),
            completionTimeout: 0.5,
            configureOutputDescriptor: { _ in
                _ = Self.waitForPIDMarkerSynchronously(pidFile, timeout: 2)
                return false
            }
        )

        await XCTAssertThrowsConnectorError(.processFailed) { try await setup.run() }

        let markerReady = Self.hasPIDMarker(pidFile)
        XCTAssertTrue(markerReady, "stub child must publish its PID before lifecycle assertions")
        guard markerReady else { return }
        let pid = try processID(from: pidFile)
        let ownedChildExited = await waitForProcessExit(pid, timeout: 0.5)
        XCTAssertTrue(ownedChildExited)
    }

    func testRunRetainsReapObligationWhenCleanupDeadlineExpires() async throws {
        let pidFile = directory.appendingPathComponent("expired-cleanup-pid.txt")
        let cli = try stubCLI(
            """
            #!/bin/sh
            trap '' TERM
            echo $$ > '\(pidFile.path)'
            while :; do sleep 0.05; done
            """
        )
        let setup = ConnectorSetup(
            cliURL: cli,
            credentialURL: try existingCredential(),
            completionTimeout: 0.01,
            configureOutputDescriptor: { descriptor in
                _ = Self.waitForPIDMarkerSynchronously(pidFile, timeout: 2)
                return Self.setNonblocking(descriptor)
            }
        )

        await XCTAssertThrowsConnectorError(.processFailed) { try await setup.run() }

        let markerReady = Self.hasPIDMarker(pidFile)
        XCTAssertTrue(markerReady, "stub child must publish its PID before lifecycle assertions")
        guard markerReady else { return }
        let pid = try processID(from: pidFile)
        let eventuallyReaped = await waitForProcessExit(pid, timeout: 0.5)
        XCTAssertTrue(eventuallyReaped)
    }

    func testRunBoundsHungProcessCompletionAndTerminatesOwnedChild() async throws {
        let pidFile = directory.appendingPathComponent("pid.txt")
        let cli = try stubCLI(
            """
            #!/bin/sh
            trap '' TERM
            echo $$ > '\(pidFile.path)'
            echo '{"configured_clients":["Codex"]}'
            while :; do sleep 0.05; done
            """
        )
        let setup = ConnectorSetup(
            cliURL: cli,
            credentialURL: try existingCredential(),
            completionTimeout: 1
        )
        let start = Date()
        let run = Task { try await setup.run() }

        let childStarted = await waitForFile(pidFile, timeout: 0.5)
        XCTAssertTrue(childStarted)
        await XCTAssertThrowsConnectorError(.processFailed) { try await run.value }

        XCTAssertLessThan(Date().timeIntervalSince(start), 1.5)
        let pid = try XCTUnwrap(Int32(String(contentsOf: pidFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)))
        let ownedChildExited = await waitForProcessExit(pid, timeout: 0.5)
        XCTAssertTrue(ownedChildExited)
    }

    func testRunBoundsReaderEOFWhenExitedChildDescendantKeepsStdoutOpen() async throws {
        let descendantReady = directory.appendingPathComponent("descendant-ready.txt")
        let releaseParent = directory.appendingPathComponent("release-parent.txt")
        let cli = try stubCLI(
            """
            #!/bin/sh
            ( printf ready > '\(descendantReady.path)'; exec /bin/sleep 2 ) &
            while [ ! -f '\(releaseParent.path)' ]; do /bin/sleep 0.01; done
            printf '{"configured_clients":["Codex"]}\n'
            exit 0
            """
        )
        let setup = ConnectorSetup(
            cliURL: cli,
            credentialURL: try existingCredential(),
            completionTimeout: 1,
            configureOutputDescriptor: { descriptor in
                guard Self.waitForFileSynchronously(descendantReady, timeout: 2) else { return false }
                do {
                    try Data().write(to: releaseParent)
                    return Self.setNonblocking(descriptor)
                } catch {
                    return false
                }
            }
        )
        let started = Date()

        await XCTAssertThrowsConnectorError(.invalidResponse) { try await setup.run() }

        XCTAssertTrue(FileManager.default.fileExists(atPath: descendantReady.path))
        XCTAssertLessThan(Date().timeIntervalSince(started), 1.5)
    }

    private func stubCLI(_ script: String) throws -> URL {
        let url = directory.appendingPathComponent("mcpaste-\(UUID().uuidString)")
        try script.write(to: url, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
        return url
    }

    private func outputCLI(_ output: String) throws -> URL {
        let encoded = Data(output.utf8).base64EncodedString()
        return try stubCLI(
            """
            #!/bin/sh
            printf '%s' '\(encoded)' | /usr/bin/base64 -D
            """
        )
    }

    private func existingCredential() throws -> URL {
        let url = directory.appendingPathComponent("credential.json")
        try #"{"endpoint":"http://127.0.0.1:38421","token":"local-token"}"#
            .write(to: url, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
        return url
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

    private func processID(from url: URL) throws -> Int32 {
        try XCTUnwrap(Int32(String(contentsOf: url, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)))
    }

    private static func waitForPIDMarkerSynchronously(_ url: URL, timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if hasPIDMarker(url) { return true }
            usleep(1_000)
        }
        return hasPIDMarker(url)
    }

    private static func waitForFileSynchronously(_ url: URL, timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if FileManager.default.fileExists(atPath: url.path) { return true }
            usleep(1_000)
        }
        return FileManager.default.fileExists(atPath: url.path)
    }

    private static func hasPIDMarker(_ url: URL) -> Bool {
        guard
            let contents = try? String(contentsOf: url, encoding: .utf8),
            Int32(contents.trimmingCharacters(in: .whitespacesAndNewlines)) != nil
        else {
            return false
        }
        return true
    }

    private static func setNonblocking(_ descriptor: Int32) -> Bool {
        let flags = fcntl(descriptor, F_GETFL)
        return flags >= 0 && fcntl(descriptor, F_SETFL, flags | O_NONBLOCK) == 0
    }
}

private func XCTAssertThrowsConnectorError<T>(
    _ expected: ConnectorSetupError,
    operation: () async throws -> T,
    file: StaticString = #filePath,
    line: UInt = #line
) async {
    do {
        _ = try await operation()
        XCTFail("Expected \(expected)", file: file, line: line)
    } catch let error as ConnectorSetupError {
        XCTAssertEqual(error, expected, file: file, line: line)
    } catch {
        XCTFail("Unexpected error type \(type(of: error))", file: file, line: line)
    }
}
