import XCTest
@testable import MCPasteApp

final class ConnectorSetupTests: XCTestCase {
    private var directory: URL!

    override func setUpWithError() throws {
        directory = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("connector-setup-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: directory)
    }

    // MARK: Short-code parsing

    func testShortCodeParsesSetupOutputLine() {
        let line = "pairing_id=abc short_code=23456789 qr_payload=mcpaste://pair/abc"
        XCTAssertEqual(ConnectorSetup.shortCode(fromSetupOutput: line), "23456789")
    }

    func testShortCodeRejectsLineWithoutCode() {
        XCTAssertNil(ConnectorSetup.shortCode(fromSetupOutput: "pairing_id=abc qr_payload=x"))
        XCTAssertNil(ConnectorSetup.shortCode(fromSetupOutput: "short_code="))
        XCTAssertNil(ConnectorSetup.shortCode(fromSetupOutput: ""))
    }

    // MARK: Credential path mirrors the CLI's DefaultCredentialPath

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

    // MARK: Process orchestration against a stub CLI

    func testRunWithoutCredentialSpawnsSetupAndApprovesPrintedCode() async throws {
        let cli = try stubCLI(
            """
            #!/bin/sh
            [ "$1" = setup ] || { echo "unexpected: $1" >&2; exit 2; }
            echo "pairing_id=abc short_code=23456789 qr_payload=mcpaste://pair/abc"
            sleep 0.2
            echo "mcpaste connector configured"
            exit 0
            """
        )
        let setup = ConnectorSetup(
            cliURL: cli,
            credentialURL: directory.appendingPathComponent("missing/credential.json"),
            deviceName: "Test Mac"
        )
        var approved: String?
        let outcome = await setup.run { approved = $0 }
        XCTAssertEqual(outcome, .configured)
        XCTAssertEqual(approved, "23456789")
    }

    func testRunWithCredentialSpawnsRegisterWithoutApproval() async throws {
        let credential = try existingCredential()
        let cli = try stubCLI(
            """
            #!/bin/sh
            [ "$1" = register ] || { echo "unexpected: $1" >&2; exit 2; }
            echo "mcpaste connector configured"
            exit 0
            """
        )
        let setup = ConnectorSetup(cliURL: cli, credentialURL: credential, deviceName: "Test Mac")
        var approverCalled = false
        let outcome = await setup.run { _ in approverCalled = true }
        XCTAssertEqual(outcome, .configured)
        XCTAssertFalse(approverCalled)
    }

    func testRunReportsMissingAIToolsDistinctly() async throws {
        let credential = try existingCredential()
        let cli = try stubCLI(
            """
            #!/bin/sh
            echo "mcpaste: \(ConnectorSetup.noAIToolsMarker)" >&2
            exit 1
            """
        )
        let setup = ConnectorSetup(cliURL: cli, credentialURL: credential, deviceName: "Test Mac")
        let outcome = await setup.run { _ in }
        XCTAssertEqual(outcome, .noAITools)
    }

    func testRunTerminatesSetupWhenApprovalFails() async throws {
        let cli = try stubCLI(
            """
            #!/bin/sh
            echo "pairing_id=abc short_code=23456789 qr_payload=x"
            sleep 30
            exit 0
            """
        )
        let setup = ConnectorSetup(
            cliURL: cli,
            credentialURL: directory.appendingPathComponent("missing/credential.json"),
            deviceName: "Test Mac"
        )
        let started = Date()
        struct ApprovalError: Error {}
        let outcome = await setup.run { _ in throw ApprovalError() }
        XCTAssertEqual(outcome, .failed)
        XCTAssertLessThan(Date().timeIntervalSince(started), 10)
    }

    func testRunFailsWhenSetupPrintsNoCode() async throws {
        let cli = try stubCLI(
            """
            #!/bin/sh
            echo "mcpaste: something went wrong" >&2
            exit 1
            """
        )
        let setup = ConnectorSetup(
            cliURL: cli,
            credentialURL: directory.appendingPathComponent("missing/credential.json"),
            deviceName: "Test Mac"
        )
        let outcome = await setup.run { _ in XCTFail("approver must not run") }
        XCTAssertEqual(outcome, .failed)
    }

    // MARK: Helpers

    private func stubCLI(_ script: String) throws -> URL {
        let url = directory.appendingPathComponent("mcpaste")
        try script.write(to: url, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
        return url
    }

    private func existingCredential() throws -> URL {
        let url = directory.appendingPathComponent("credential.json")
        try #"{"endpoint":"https://example.invalid/v1/mcp","token":"example-token-not-real"}"#
            .write(to: url, atomically: true, encoding: .utf8)
        return url
    }
}
