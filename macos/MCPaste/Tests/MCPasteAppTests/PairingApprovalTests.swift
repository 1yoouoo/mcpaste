import XCTest
import MCPasteCore
@testable import MCPasteApp

/// A pairing admin API that records calls; responses are decoded from wire-shaped
/// JSON because the pairing records are Decodable-only.
private final class RecordingPairingAPI: PairingAdminAPI {
    var lookedUpCodes: [String] = []
    var approvedIDs: [String] = []
    var deniedIDs: [String] = []
    var nextLookup: PairingDetails?
    var failNextAction = false

    func lookupPairing(shortCode: String) async throws -> PairingDetails {
        lookedUpCodes.append(shortCode)
        guard let nextLookup else { throw APIError.notFound }
        return nextLookup
    }

    func approvePairing(id: String, idempotencyKey: String) async throws -> ApprovalRecord {
        if failNextAction { throw APIError.http(status: 500) }
        approvedIDs.append(id)
        return try decode("""
        {"pairing_id":"\(id)","status":"approved","claim_expires_at":"2026-08-18T13:00:00Z"}
        """)
    }

    func denyPairing(id: String, idempotencyKey: String) async throws -> PairingStatusRecord {
        if failNextAction { throw APIError.http(status: 500) }
        deniedIDs.append(id)
        return try decode("""
        {"pairing_id":"\(id)","status":"denied","expires_at":"2026-08-18T13:00:00Z","claim_expires_at":null}
        """)
    }
}

private func decode<T: Decodable>(_ json: String) throws -> T {
    let decoder = JSONDecoder()
    decoder.dateDecodingStrategy = .iso8601
    return try decoder.decode(T.self, from: Data(json.utf8))
}

private func pairingDetails(status: String = "pending", scope: String = "connector") throws -> PairingDetails {
    try decode("""
    {"pairing_id":"pr-1","proposed_name":"mac-mini connector","platform":"linux",
     "requested_scope":"\(scope)","status":"\(status)",
     "expires_at":"2026-08-18T12:00:00Z","claim_expires_at":null}
    """)
}

@MainActor
final class PairingApprovalTests: XCTestCase {
    private func makeModel(api: RecordingPairingAPI) -> AppModel {
        let model = AppModel(keychain: KeychainStore(service: "com.mcpaste.tests.\(UUID().uuidString)"))
        model.installPairingAPIForTesting(api)
        return model
    }

    func testLookupShowsThePendingRequest() async throws {
        let api = RecordingPairingAPI()
        api.nextLookup = try pairingDetails()
        let model = makeModel(api: api)

        await model.lookupPairing(shortCode: "  AB12-CD34  ")

        XCTAssertEqual(api.lookedUpCodes, ["AB12-CD34"], "the code is trimmed before it goes to the server")
        XCTAssertEqual(model.pendingApproval?.proposedName, "mac-mini connector")
        XCTAssertNil(model.pairingNotice)
    }

    func testLookupRejectsARequestThatIsNoLongerPending() async throws {
        let api = RecordingPairingAPI()
        api.nextLookup = try pairingDetails(status: "approved")
        let model = makeModel(api: api)

        await model.lookupPairing(shortCode: "AB12")

        XCTAssertNil(model.pendingApproval, "an already-decided request offers nothing to approve")
        XCTAssertEqual(model.pairingNotice, "That request is already approved.")
    }

    func testUnknownCodeShowsAFriendlyNotice() async {
        let api = RecordingPairingAPI()
        let model = makeModel(api: api)

        await model.lookupPairing(shortCode: "NOPE")

        XCTAssertNil(model.pendingApproval)
        XCTAssertEqual(model.pairingNotice, "No pending request matches that code.")
    }

    func testEmptyCodeNeverReachesTheServer() async {
        let api = RecordingPairingAPI()
        let model = makeModel(api: api)

        await model.lookupPairing(shortCode: "   ")

        XCTAssertTrue(api.lookedUpCodes.isEmpty)
        XCTAssertEqual(model.pairingNotice, "Enter the code shown on the other device.")
    }

    func testApproveSendsThePairingIDAndClearsTheRequest() async throws {
        let api = RecordingPairingAPI()
        api.nextLookup = try pairingDetails()
        let model = makeModel(api: api)
        await model.lookupPairing(shortCode: "AB12")

        await model.approvePendingPairing()

        XCTAssertEqual(api.approvedIDs, ["pr-1"])
        XCTAssertNil(model.pendingApproval)
        XCTAssertEqual(model.pairingNotice, "mac-mini connector approved — it connects on its next claim.")
    }

    func testDenySendsThePairingIDAndClearsTheRequest() async throws {
        let api = RecordingPairingAPI()
        api.nextLookup = try pairingDetails()
        let model = makeModel(api: api)
        await model.lookupPairing(shortCode: "AB12")

        await model.denyPendingPairing()

        XCTAssertEqual(api.deniedIDs, ["pr-1"])
        XCTAssertNil(model.pendingApproval)
        XCTAssertEqual(model.pairingNotice, "mac-mini connector denied.")
    }

    func testFailedApprovalKeepsTheRequestOnScreen() async throws {
        let api = RecordingPairingAPI()
        api.nextLookup = try pairingDetails()
        let model = makeModel(api: api)
        await model.lookupPairing(shortCode: "AB12")
        api.failNextAction = true

        await model.approvePendingPairing()

        XCTAssertNotNil(model.pendingApproval, "a failed call must stay approvable — losing it would strand the other device")
        XCTAssertEqual(model.pairingNotice, "The request could not be approved.")
    }

    func testScopeLabelNamesTheConnectorPlainly() {
        XCTAssertEqual(AppModel.pairingScopeLabel("connector"), "MCP connector (read-only)")
        XCTAssertEqual(AppModel.pairingScopeLabel("full"), "Full access")
        XCTAssertEqual(AppModel.pairingScopeLabel("future-scope"), "future-scope", "unknown scopes pass through rather than mislabel")
    }
}
