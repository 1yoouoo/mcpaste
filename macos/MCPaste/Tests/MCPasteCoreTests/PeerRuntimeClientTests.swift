import Foundation
import XCTest
@testable import MCPasteCore

final class PeerRuntimeClientTests: XCTestCase {
    private let token = "local-test-token-secret"

    override func tearDown() {
        RuntimeURLProtocol.reset()
        super.tearDown()
    }

    func testRuntimeRevisionComparableUsesWallLogicalDeviceTupleOrder() {
        let revisions = [
            RuntimeRevision(wallMillis: 2, logical: 2, deviceID: "device-a"),
            RuntimeRevision(wallMillis: 2, logical: 1, deviceID: "device-b"),
            RuntimeRevision(wallMillis: 1, logical: 9, deviceID: "device-z"),
            RuntimeRevision(wallMillis: 2, logical: 1, deviceID: "device-a")
        ]

        XCTAssertEqual(revisions.sorted(), [
            RuntimeRevision(wallMillis: 1, logical: 9, deviceID: "device-z"),
            RuntimeRevision(wallMillis: 2, logical: 1, deviceID: "device-a"),
            RuntimeRevision(wallMillis: 2, logical: 1, deviceID: "device-b"),
            RuntimeRevision(wallMillis: 2, logical: 2, deviceID: "device-a")
        ])
    }

    func testRuntimeRevisionAndPeerDeviceEncodeExactSnakeCaseKeys() throws {
        let revision = RuntimeRevision(wallMillis: 42, logical: 3, deviceID: "device-a")
        let revisionObject = try XCTUnwrap(
            JSONSerialization.jsonObject(with: JSONEncoder().encode(revision)) as? [String: Any]
        )
        XCTAssertEqual(Set(revisionObject.keys), ["wall_millis", "logical", "device_id"])
        XCTAssertEqual(revisionObject["wall_millis"] as? Int64, 42)
        XCTAssertEqual(revisionObject["logical"] as? UInt32, 3)
        XCTAssertEqual(revisionObject["device_id"] as? String, "device-a")

        let device = PeerDevice(
            id: "device-a",
            displayName: "Desk Mac",
            reachable: true,
            isLocal: true,
            isSource: false,
            lastSeenAt: Date(timeIntervalSince1970: 0)
        )
        let deviceObject = try XCTUnwrap(
            JSONSerialization.jsonObject(with: JSONEncoder().encode(device)) as? [String: Any]
        )
        XCTAssertEqual(Set(deviceObject.keys), [
            "id", "display_name", "reachable", "is_local", "is_source", "last_seen_at"
        ])

        let publication = PublicationResult(revision: revision, syncState: .waitingToSync)
        let publicationObject = try XCTUnwrap(
            JSONSerialization.jsonObject(with: JSONEncoder().encode(publication)) as? [String: Any]
        )
        XCTAssertEqual(Set(publicationObject.keys), ["revision", "sync_state"])
        XCTAssertEqual(publicationObject["sync_state"] as? String, "waiting_to_sync")
    }

    func testCurrentPreservesExactCRLFAndTrailingTextAndIgnoresUnknownFields() async throws {
        let client = try makeClient { request in
            XCTAssertEqual(request.url?.path, "/v1/local/context")
            return .json(
                #"{"protocol_version":1,"revision":{"wall_millis":42,"logical":3,"device_id":"device-a","future_revision_field":true},"source_device_id":"device-a","updated_at":"2026-08-18T01:02:03.456Z","text":"line 1\r\nline 2  ","assets":[],"source_reachable":true,"sync_state":"up_to_date","future":"ignored"}"#
            )
        }

        let loaded = try await client.current()
        let context = try XCTUnwrap(loaded)

        XCTAssertEqual(context.text, "line 1\r\nline 2  ")
        XCTAssertEqual(context.revision, RuntimeRevision(wallMillis: 42, logical: 3, deviceID: "device-a"))
        XCTAssertEqual(context.syncState, .upToDate)
    }

    func testCurrentDecodesMaximumEscapeHeavyManifestExactly() async throws {
        let maximumTextBytes = 4 * 1024 * 1024
        let soundJSONBound = maximumTextBytes * 6 + 64 * 1024
        let text = String(repeating: "\u{0001}", count: maximumTextBytes)
        let body = try JSONSerialization.data(withJSONObject: [
            "protocol_version": 1,
            "revision": ["wall_millis": 42, "logical": 3, "device_id": "device-a"],
            "source_device_id": "device-a",
            "updated_at": "2026-08-18T01:02:03.456Z",
            "text": text,
            "assets": [],
            "source_reachable": true,
            "sync_state": "up_to_date"
        ])
        XCTAssertGreaterThan(body.count, maximumTextBytes + 16 * 1024)
        XCTAssertLessThanOrEqual(body.count, soundJSONBound)
        let client = try makeClient { _ in
            .init(
                statusCode: 200,
                headers: ["Content-Type": "application/json", "Content-Length": String(body.count)],
                body: body
            )
        }

        let loadedContext = try await client.current()
        let context = try XCTUnwrap(loadedContext)

        XCTAssertEqual(context.text, text)
        XCTAssertEqual(context.text.lengthOfBytes(using: .utf8), maximumTextBytes)
    }

    func testCurrentDownloadsAssetsConcurrentlyButReturnsManifestOrder() async throws {
        let first = Data([1, 2, 3])
        let second = Data([4, 5, 6])
        let firstDigest = "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81"
        let secondDigest = "787c798e39a5bc1910355bae6d0cd87a36b2e10fd0202a83e3bb6b005da83472"
        let client = try makeClient { request in
            switch request.url?.path {
            case "/v1/local/context":
                return .json(Self.manifest(assets: [
                    Self.assetJSON(digest: firstDigest, mime: "image/png", size: 3),
                    Self.assetJSON(digest: secondDigest, mime: "image/jpeg", size: 3)
                ]))
            case "/v1/local/context/assets/0":
                Thread.sleep(forTimeInterval: 0.08)
                return .asset(first, mime: "image/png", digest: firstDigest)
            case "/v1/local/context/assets/1":
                return .asset(second, mime: "image/jpeg", digest: secondDigest)
            default:
                return .init(statusCode: 404)
            }
        }

        let loaded = try await client.current()
        let context = try XCTUnwrap(loaded)

        XCTAssertEqual(context.assets.map(\.digest), [firstDigest, secondDigest])
        XCTAssertEqual(context.assets.map(\.data), [first, second])
    }

    func testSourceOfflineReturnsValidatedTextAndDownloadedAssetsInManifestOrder() async throws {
        let requests = LockedRequests()
        let first = Data([1, 2, 3])
        let second = Data([4, 5, 6])
        let firstDigest = "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81"
        let secondDigest = "787c798e39a5bc1910355bae6d0cd87a36b2e10fd0202a83e3bb6b005da83472"
        let client = try makeClient { request in
            requests.append(request)
            switch request.url?.path {
            case "/v1/local/context":
                return .json(Self.manifest(
                    text: "offline replica",
                    assets: [
                        Self.assetJSON(digest: firstDigest, mime: "image/png", size: 3),
                        Self.assetJSON(digest: secondDigest, mime: "image/jpeg", size: 3)
                    ],
                    sourceReachable: false,
                    syncState: "source_offline"
                ))
            case "/v1/local/context/assets/0":
                return .asset(first, mime: "image/png", digest: firstDigest)
            case "/v1/local/context/assets/1":
                return .asset(second, mime: "image/jpeg", digest: secondDigest)
            default:
                return .init(statusCode: 404)
            }
        }

        let loaded = try await client.current()
        let context = try XCTUnwrap(loaded)

        XCTAssertEqual(context.text, "offline replica")
        XCTAssertFalse(context.sourceReachable)
        XCTAssertEqual(context.syncState, .sourceOffline)
        XCTAssertEqual(context.assets.map(\.digest), [firstDigest, secondDigest])
        XCTAssertEqual(context.assets.map(\.data), [first, second])
        XCTAssertEqual(requests.paths.first, "/v1/local/context")
        XCTAssertEqual(Set(requests.paths.dropFirst()), [
            "/v1/local/context/assets/0",
            "/v1/local/context/assets/1"
        ])
    }

    func testCurrentMapsNotFoundToExactEmptyError() async throws {
        let client = try makeClient { _ in .init(statusCode: 404) }

        await XCTAssertThrowsPeerError(.empty) { try await client.current() }
    }

    func testManifestRejectsDeclaredAndStreamingBodiesOverLimit() async throws {
        let declared = try makeClient { _ in
            .init(statusCode: 200, headers: ["Content-Length": String(5 * 1024 * 1024)], body: Data())
        }
        await XCTAssertThrowsPeerError(.invalidResponse) { try await declared.current() }

        let streaming = try makeClient { _ in
            .init(statusCode: 200, body: Data(repeating: 0x61, count: 5 * 1024 * 1024))
        }
        await XCTAssertThrowsPeerError(.invalidResponse) { try await streaming.current() }
    }

    func testRequestsAndFixedErrorsNeverExposeTokenURLPathOrResponseBody() async throws {
        let requests = LockedRequests()
        let responseSecret = "response-body-secret"
        let client = try makeClient { request in
            requests.append(request)
            return .init(statusCode: 500, body: Data(responseSecret.utf8))
        }

        do {
            _ = try await client.current()
            XCTFail("Expected unavailable")
        } catch let error as PeerRuntimeError {
            XCTAssertEqual(error, .unavailable)
            let rendered = String(describing: error)
            XCTAssertFalse(rendered.contains(token))
            XCTAssertFalse(rendered.contains(responseSecret))
            XCTAssertFalse(rendered.contains("/v1/local/context"))
            XCTAssertFalse(rendered.contains("127.0.0.1"))
        }
        XCTAssertTrue(requests.urls.allSatisfy { !$0.absoluteString.contains(token) && $0.query == nil })
    }

    func testDevicesMapsSnakeCaseAndIgnoresUnknownFields() async throws {
        let client = try makeClient { request in
            XCTAssertEqual(request.url?.path, "/v1/local/devices")
            return .json(
                #"{"devices":[{"id":"local-id","display_name":"Desk Mac","reachable":true,"is_local":true,"is_source":false,"last_seen_at":"2026-08-18T01:02:03.456Z","future":1},{"id":"peer-id","display_name":"Laptop","reachable":false,"is_local":false,"is_source":true,"last_seen_at":"2026-08-17T00:00:00Z"}],"future":true}"#
            )
        }

        let devices = try await client.devices()

        XCTAssertEqual(devices.map(\.id), ["local-id", "peer-id"])
        XCTAssertEqual(devices.map(\.displayName), ["Desk Mac", "Laptop"])
        XCTAssertEqual(devices.map(\.reachable), [true, false])
        XCTAssertEqual(devices.map(\.isSource), [false, true])
    }

    func testRedirectIsRefusedWithoutFollowingLocation() async throws {
        let requests = LockedRequests()
        let client = try makeClient { request in
            requests.append(request)
            return .redirect(to: URL(string: "http://127.0.0.1:38421/redirected")!)
        }

        let started = Date()
        await XCTAssertThrowsPeerError(.unavailable) { try await client.devices() }

        XCTAssertEqual(requests.paths, ["/v1/local/devices"])
        XCTAssertLessThan(Date().timeIntervalSince(started), 0.5)
    }

    func testMismatchedResponseURLIsRejected() async throws {
        let client = try makeClient { _ in
            .init(
                statusCode: 200,
                body: Data(#"{"devices":[]}"#.utf8),
                responseURL: URL(string: "http://127.0.0.1:38421/different")
            )
        }

        await XCTAssertThrowsPeerError(.invalidResponse) { try await client.devices() }
    }

    func testDevicesResponseRejectsDeclaredAndStreamingBodiesOverLimit() async throws {
        let declared = try makeClient { _ in
            .init(statusCode: 200, headers: ["Content-Length": String(1024 * 1024 + 1)])
        }
        await XCTAssertThrowsPeerError(.invalidResponse) { try await declared.devices() }

        RuntimeURLProtocol.reset()
        let streaming = try makeClient { _ in
            .init(statusCode: 200, body: Data(repeating: 0x61, count: 1024 * 1024 + 1))
        }
        await XCTAssertThrowsPeerError(.invalidResponse) { try await streaming.devices() }
    }

    func testAssetCountOverEightIsRejectedBeforeAssetFetch() async throws {
        let requests = LockedRequests()
        let digest = "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81"
        let client = try makeClient { request in
            requests.append(request)
            return .json(Self.manifest(assets: Array(
                repeating: Self.assetJSON(digest: digest, mime: "image/png", size: 3),
                count: 9
            )))
        }

        await XCTAssertThrowsPeerError(.invalidResponse) { try await client.current() }

        XCTAssertEqual(requests.paths, ["/v1/local/context"])
    }

    func testPerAssetLimitIsRejectedBeforeAssetFetch() async throws {
        let requests = LockedRequests()
        let digest = "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81"
        let client = try makeClient { request in
            requests.append(request)
            return .json(Self.manifest(assets: [
                Self.assetJSON(digest: digest, mime: "image/png", size: 8 * 1024 * 1024 + 1)
            ]))
        }

        await XCTAssertThrowsPeerError(.invalidResponse) { try await client.current() }

        XCTAssertEqual(requests.paths, ["/v1/local/context"])
    }

    func testTextLimitIsRejectedBeforeAssetFetch() async throws {
        let requests = LockedRequests()
        let digest = "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81"
        let client = try makeClient { request in
            requests.append(request)
            return .json(Self.manifest(
                text: String(repeating: "x", count: 4 * 1024 * 1024 + 1),
                assets: [Self.assetJSON(digest: digest, mime: "image/png", size: 3)]
            ))
        }

        await XCTAssertThrowsPeerError(.invalidResponse) { try await client.current() }

        XCTAssertEqual(requests.paths, ["/v1/local/context"])
    }

    func testCompleteBundleLimitIsRejectedBeforeAssetFetch() async throws {
        let requests = LockedRequests()
        let digest = "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81"
        let client = try makeClient { request in
            requests.append(request)
            return .json(Self.manifest(
                text: "x",
                assets: Array(
                    repeating: Self.assetJSON(digest: digest, mime: "image/png", size: 8 * 1024 * 1024),
                    count: 4
                )
            ))
        }

        await XCTAssertThrowsPeerError(.invalidResponse) { try await client.current() }

        XCTAssertEqual(requests.paths, ["/v1/local/context"])
    }

    func testAssetResponseRejectsDeclaredAndStreamingOverflowWithoutReturningContext() async throws {
        let digest = "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81"
        let responses: [RuntimeStubResponse] = [
            .init(statusCode: 200, headers: ["Content-Length": String(8 * 1024 * 1024 + 1)]),
            .init(
                statusCode: 200,
                headers: ["Content-Type": "image/png", "X-MCPaste-SHA256": digest],
                body: Data(repeating: 0x01, count: 8 * 1024 * 1024 + 1)
            )
        ]
        for response in responses {
            RuntimeURLProtocol.reset()
            let client = try makeClient { request in
                if request.url?.path == "/v1/local/context" {
                    return .json(Self.manifest(assets: [
                        Self.assetJSON(digest: digest, mime: "image/png", size: 8 * 1024 * 1024)
                    ]))
                }
                return response
            }

            await XCTAssertThrowsPeerError(.invalidResponse) { try await client.current() }
        }
    }

    func testAssetNonSuccessDoesNotReturnPartialOrStaleContext() async throws {
        let digest = "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81"
        let client = try makeClient { request in
            if request.url?.path == "/v1/local/context" {
                return .json(Self.manifest(assets: [
                    Self.assetJSON(digest: digest, mime: "image/png", size: 3)
                ]))
            }
            return .init(statusCode: 503, body: Data("not-an-asset".utf8))
        }

        await XCTAssertThrowsPeerError(.invalidResponse) { try await client.current() }
    }

    func testPublishStagesImagesWithExactHeadersThenPublishesOrderedDigestsAndText() async throws {
        let requests = LockedRequests()
        let first = NormalizedImage(mimeType: "image/png", width: 11, height: 12, data: Data([1, 2, 3]))
        let second = NormalizedImage(mimeType: "image/jpeg", width: 21, height: 22, data: Data([4, 5, 6]))
        let firstDigest = "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81"
        let secondDigest = "787c798e39a5bc1910355bae6d0cd87a36b2e10fd0202a83e3bb6b005da83472"
        let expected = RuntimeRevision(wallMillis: 42, logical: 3, deviceID: "device-a")
        let committed = RuntimeRevision(wallMillis: 43, logical: 0, deviceID: "local")
        let client = try makeClient { request in
            requests.append(request)
            if request.url?.path == "/v1/local/context" {
                return .json(Self.publicationJSON(revision: committed, syncState: "updating"))
            }
            return .init(statusCode: 204)
        }

        let result = try await client.publish(
            text: "exact\r\ntext  ",
            images: [first, second],
            expectedRevision: expected
        )

        XCTAssertEqual(requests.paths, [
            "/v1/local/assets/\(firstDigest)",
            "/v1/local/assets/\(secondDigest)",
            "/v1/local/context"
        ])
        let captured = requests.values
        XCTAssertEqual(captured.map { $0.value(forHTTPHeaderField: "Authorization") }, Array(repeating: "Bearer \(token)", count: 3))
        XCTAssertEqual(captured[0].httpMethod, "PUT")
        XCTAssertEqual(captured[0].value(forHTTPHeaderField: "Content-Type"), "image/png")
        XCTAssertEqual(captured[0].value(forHTTPHeaderField: "Content-Length"), "3")
        XCTAssertEqual(captured[0].value(forHTTPHeaderField: "X-MCPaste-Width"), "11")
        XCTAssertEqual(captured[0].value(forHTTPHeaderField: "X-MCPaste-Height"), "12")
        XCTAssertEqual(captured[0].httpBody, first.data)
        XCTAssertEqual(captured[1].value(forHTTPHeaderField: "Content-Type"), "image/jpeg")
        XCTAssertEqual(captured[1].value(forHTTPHeaderField: "X-MCPaste-Width"), "21")
        XCTAssertEqual(captured[1].value(forHTTPHeaderField: "X-MCPaste-Height"), "22")
        XCTAssertEqual(captured[1].httpBody, second.data)
        let body = try XCTUnwrap(captured[2].httpBody)
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: Any])
        XCTAssertEqual(object["text"] as? String, "exact\r\ntext  ")
        XCTAssertEqual(object["asset_digests"] as? [String], [firstDigest, secondDigest])
        let expectedObject = try XCTUnwrap(object["expected_revision"] as? [String: Any])
        XCTAssertEqual(expectedObject["wall_millis"] as? Int, 42)
        XCTAssertEqual(expectedObject["logical"] as? Int, 3)
        XCTAssertEqual(expectedObject["device_id"] as? String, "device-a")
        XCTAssertEqual(Set(object.keys), ["text", "asset_digests", "expected_revision"])
        XCTAssertEqual(result, PublicationResult(revision: committed, syncState: .updating))
    }

    func testPublishSendsMaximumEscapeHeavyTextWithinSoundJSONBound() async throws {
        let maximumTextBytes = 4 * 1024 * 1024
        let soundJSONBound = maximumTextBytes * 6 + 64 * 1024
        let text = String(repeating: "\u{0001}", count: maximumTextBytes)
        let committed = RuntimeRevision(wallMillis: 1, logical: 0, deviceID: "local")
        let requests = LockedRequests()
        let client = try makeClient { request in
            requests.append(request)
            return .json(Self.publicationJSON(revision: committed, syncState: "up_to_date"))
        }

        let result = try await client.publish(text: text, images: [], expectedRevision: nil)

        let body = try XCTUnwrap(requests.values.first?.httpBody)
        XCTAssertLessThanOrEqual(body.count, soundJSONBound)
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: Any])
        XCTAssertEqual(object["text"] as? String, text)
        XCTAssertEqual((object["text"] as? String)?.lengthOfBytes(using: .utf8), maximumTextBytes)
        XCTAssertEqual(result, PublicationResult(revision: committed, syncState: .upToDate))
    }

    func testPublishEncodesExplicitNullExpectedRevision() async throws {
        let committed = RuntimeRevision(wallMillis: 1, logical: 0, deviceID: "local")
        let requests = LockedRequests()
        let client = try makeClient { request in
            requests.append(request)
            return .json(Self.publicationJSON(revision: committed, syncState: "up_to_date"))
        }

        _ = try await client.publish(text: "first", images: [], expectedRevision: nil)

        let body = try XCTUnwrap(requests.values.first?.httpBody)
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: Any])
        XCTAssertTrue(object["expected_revision"] is NSNull)
        XCTAssertEqual(Set(object.keys), ["text", "asset_digests", "expected_revision"])
    }

    func testPublishAcceptsOnlyAllowedPublicationStates() async throws {
        let revision = RuntimeRevision(wallMillis: 5, logical: 1, deviceID: "local")
        for state in ["up_to_date", "updating", "waiting_to_sync"] {
            RuntimeURLProtocol.reset()
            let client = try makeClient { _ in
                .json(Self.publicationJSON(revision: revision, syncState: state))
            }

            let result = try await client.publish(text: state, images: [], expectedRevision: nil)

            XCTAssertEqual(result.revision, revision)
            XCTAssertEqual(result.syncState.rawValue, state)
        }
    }

    func testPublishRejectsConflictNonSuccessMalformedAndOfflineResponsesWithFixedErrors() async throws {
        let cases: [(RuntimeStubResponse, PeerRuntimeError)] = [
            (.init(statusCode: 409, body: Data("secret conflict".utf8)), .conflict),
            (.init(statusCode: 503, body: Data("secret unavailable".utf8)), .unavailable),
            (.json("not-json"), .invalidResponse),
            (.json(Self.publicationJSON(
                revision: RuntimeRevision(wallMillis: 1, logical: 0, deviceID: "local"),
                syncState: "source_offline"
            )), .invalidResponse),
            (.json(Self.publicationJSON(
                revision: RuntimeRevision(wallMillis: 1, logical: 0, deviceID: "local"),
                syncState: "invalid"
            )), .invalidResponse)
        ]
        for (response, expectedError) in cases {
            RuntimeURLProtocol.reset()
            let client = try makeClient { _ in response }
            await XCTAssertThrowsPeerError(expectedError) {
                try await client.publish(text: "safe", images: [], expectedRevision: nil)
            }
        }
    }

    func testCurrentNeverRunsMoreThanThreeAssetDownloadsAtOnce() async throws {
        let concurrency = LockedConcurrency()
        let bytes = [Data([1, 2, 3]), Data([4, 5, 6]), Data([7, 8, 9]), Data([10]), Data([11]), Data([12])]
        let digests = [
            "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81",
            "787c798e39a5bc1910355bae6d0cd87a36b2e10fd0202a83e3bb6b005da83472",
            "66a6757151f8ee55db127716c7e3dce0be8074b64e20eda542e5c1e46ca9c41e",
            "01ba4719c80b6fe911b091a7c05124b64eeece964e09c058ef8f9805daca546b",
            "e7cf46a078fed4fafd0b5e3aff144802b853f8ae459a4f0c14add3314b7cc3a6",
            "ef6cbd2161eaea7943ce8693b9824d23d1793ffb1c0fca05b600d3899b44c977"
        ]
        let client = try makeClient { request in
            if request.url?.path == "/v1/local/context" {
                return .json(Self.manifest(assets: zip(digests, bytes).map {
                    Self.assetJSON(digest: $0.0, mime: "image/png", size: $0.1.count)
                }))
            }
            let index = Int(request.url!.lastPathComponent)!
            concurrency.enter()
            Thread.sleep(forTimeInterval: 0.06)
            concurrency.leave()
            return .asset(bytes[index], mime: "image/png", digest: digests[index])
        }

        _ = try await client.current()

        XCTAssertEqual(concurrency.maximum, 3)
    }

    func testFailedAssetCancelsInitialFanoutAndNeverStartsFourthRequest() async throws {
        let requests = LockedRequests()
        let blockersStarted = DispatchSemaphore(value: 0)
        let digests = [
            "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81",
            "787c798e39a5bc1910355bae6d0cd87a36b2e10fd0202a83e3bb6b005da83472",
            "66a6757151f8ee55db127716c7e3dce0be8074b64e20eda542e5c1e46ca9c41e",
            "01ba4719c80b6fe911b091a7c05124b64eeece964e09c058ef8f9805daca546b"
        ]
        let client = try makeClient { request in
            requests.append(request)
            if request.url?.path == "/v1/local/context" {
                return .json(Self.manifest(assets: digests.map {
                    Self.assetJSON(digest: $0, mime: "image/png", size: 3)
                }))
            }
            switch request.url?.lastPathComponent {
            case "0":
                _ = blockersStarted.wait(timeout: .now() + 1)
                _ = blockersStarted.wait(timeout: .now() + 1)
                return .init(statusCode: 503)
            case "1", "2":
                blockersStarted.signal()
                return .waitingForCancellation()
            default:
                return .init(statusCode: 500)
            }
        }

        await XCTAssertThrowsPeerError(.invalidResponse) { try await client.current() }

        XCTAssertFalse(requests.paths.contains("/v1/local/context/assets/3"))
        let stopDeadline = Date().addingTimeInterval(0.2)
        while RuntimeURLProtocol.stopCount < 2 && Date() < stopDeadline {
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
        XCTAssertGreaterThanOrEqual(RuntimeURLProtocol.stopCount, 2)
    }

    func testAssetMetadataLengthAndDigestAreAllVerified() async throws {
        let body = Data([1, 2, 3])
        let digest = "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81"
        let cases: [(String, RuntimeStubResponse)] = [
            ("mime", .asset(body, mime: "image/jpeg", digest: digest)),
            ("declared length", .init(statusCode: 200, headers: ["Content-Type": "image/png", "Content-Length": "2", "X-MCPaste-SHA256": digest], body: body)),
            ("digest header", .asset(body, mime: "image/png", digest: String(repeating: "0", count: 64))),
            ("body digest", .asset(Data([3, 2, 1]), mime: "image/png", digest: digest))
        ]
        for (name, response) in cases {
            RuntimeURLProtocol.reset()
            let client = try makeClient { request in
                request.url?.path == "/v1/local/context"
                    ? .json(Self.manifest(assets: [Self.assetJSON(digest: digest, mime: "image/png", size: 3)]))
                    : response
            }
            do {
                _ = try await client.current()
                XCTFail("Expected invalid response for \(name)")
            } catch let error as PeerRuntimeError {
                XCTAssertEqual(error, .invalidResponse, name)
            }
        }
    }

    func testNonASCIIAssetDigestIsRejectedBeforeAssetFetch() async throws {
        let requests = LockedRequests()
        let nonASCII = String(repeating: "١", count: 64)
        let client = try makeClient { request in
            requests.append(request)
            if request.url?.path == "/v1/local/context" {
                return .json(Self.manifest(assets: [
                    Self.assetJSON(digest: nonASCII, mime: "image/png", size: 3)
                ]))
            }
            return .init(statusCode: 404)
        }

        await XCTAssertThrowsPeerError(.invalidResponse) { try await client.current() }

        XCTAssertEqual(requests.paths, ["/v1/local/context"])
    }

    func testMalformedTransportStatusAndInvalidImageMapToFixedErrors() async throws {
        let malformed = try makeClient { _ in .json("not-json") }
        await XCTAssertThrowsPeerError(.invalidResponse) { try await malformed.current() }

        RuntimeURLProtocol.reset()
        let transport = try makeClient { _ in throw URLError(.cannotConnectToHost) }
        await XCTAssertThrowsPeerError(.unavailable) { try await transport.devices() }

        RuntimeURLProtocol.reset()
        let status = try makeClient { _ in .init(statusCode: 401, body: Data("secret".utf8)) }
        await XCTAssertThrowsPeerError(.unavailable) { try await status.devices() }

        RuntimeURLProtocol.reset()
        let publisher = try makeClient { _ in .init(statusCode: 204) }
        let invalid = NormalizedImage(mimeType: "text/plain", width: 1, height: 1, data: Data([1]))
        await XCTAssertThrowsPeerError(.rejectedImage) {
            try await publisher.publish(text: "", images: [invalid], expectedRevision: nil)
        }
    }

    func testRejectsNonExactLoopbackEndpointAndConfiguredProxy() throws {
        let session = makeSession()
        XCTAssertThrowsError(try PeerRuntimeClient(baseURL: URL(string: "http://localhost:38421")!, token: token, session: session))
        XCTAssertThrowsError(try PeerRuntimeClient(baseURL: URL(string: "http://127.0.0.1:38421/path")!, token: token, session: session))

        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [RuntimeURLProtocol.self]
        configuration.connectionProxyDictionary = ["HTTPEnable": 1, "HTTPProxy": "127.0.0.2", "HTTPPort": 8080]
        XCTAssertThrowsError(try PeerRuntimeClient(
            baseURL: URL(string: "http://127.0.0.1:38421")!,
            token: token,
            session: URLSession(configuration: configuration)
        ))
    }

    func testTokenUTF8ByteLimitAccepts4096AndRejects4097() throws {
        let session = makeSession()
        let endpoint = URL(string: "http://127.0.0.1:38421")!

        XCTAssertNoThrow(try PeerRuntimeClient(
            baseURL: endpoint,
            token: String(repeating: "x", count: 4 * 1024),
            session: session
        ))
        XCTAssertNoThrow(try PeerRuntimeClient(
            baseURL: endpoint,
            token: String(repeating: "é", count: 2 * 1024),
            session: session
        ))
        XCTAssertThrowsError(try PeerRuntimeClient(
            baseURL: endpoint,
            token: String(repeating: "x", count: 4 * 1024 + 1),
            session: session
        )) { error in
            XCTAssertEqual(error as? PeerRuntimeError, .invalidResponse)
        }
        XCTAssertThrowsError(try PeerRuntimeClient(
            baseURL: endpoint,
            token: String(repeating: "é", count: 2 * 1024 + 1),
            session: session
        )) { error in
            XCTAssertEqual(error as? PeerRuntimeError, .invalidResponse)
        }
    }

    private func makeClient(
        handler: @escaping @Sendable (URLRequest) throws -> RuntimeStubResponse
    ) throws -> PeerRuntimeClient {
        RuntimeURLProtocol.setHandler(handler)
        return try PeerRuntimeClient(
            baseURL: URL(string: "http://127.0.0.1:38421")!,
            token: token,
            session: makeSession()
        )
    }

    private func makeSession() -> URLSession {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [RuntimeURLProtocol.self]
        configuration.connectionProxyDictionary = [:]
        return URLSession(configuration: configuration)
    }

    private static func manifest(
        text: String = "exact",
        assets: [String],
        sourceReachable: Bool = true,
        syncState: String = "up_to_date"
    ) -> String {
        """
        {"protocol_version":1,"revision":{"wall_millis":42,"logical":3,"device_id":"device-a"},"source_device_id":"device-a","updated_at":"2026-08-18T01:02:03.456Z","text":"\(text)","assets":[\(assets.joined(separator: ","))],"source_reachable":\(sourceReachable),"sync_state":"\(syncState)"}
        """
    }

    private static func assetJSON(digest: String, mime: String, size: Int) -> String {
        #"{"sha256":"\#(digest)","mime_type":"\#(mime)","width":1,"height":1,"byte_size":\#(size),"future":true}"#
    }

    private static func publicationJSON(revision: RuntimeRevision, syncState: String) -> String {
        """
        {"revision":{"wall_millis":\(revision.wallMillis),"logical":\(revision.logical),"device_id":"\(revision.deviceID)"},"sync_state":"\(syncState)"}
        """
    }
}

private func XCTAssertThrowsPeerError<T>(
    _ expected: PeerRuntimeError,
    operation: () async throws -> T,
    file: StaticString = #filePath,
    line: UInt = #line
) async {
    do {
        _ = try await operation()
        XCTFail("Expected \(expected)", file: file, line: line)
    } catch let error as PeerRuntimeError {
        XCTAssertEqual(error, expected, file: file, line: line)
    } catch {
        XCTFail("Unexpected error type \(type(of: error))", file: file, line: line)
    }
}

private struct RuntimeStubResponse: Sendable {
    var statusCode: Int
    var headers: [String: String] = [:]
    var body = Data()
    var redirectURL: URL? = nil
    var responseURL: URL? = nil
    var waitsForCancellation = false

    static func json(_ value: String) -> Self {
        .init(statusCode: 200, headers: ["Content-Type": "application/json"], body: Data(value.utf8))
    }

    static func asset(_ data: Data, mime: String, digest: String) -> Self {
        .init(statusCode: 200, headers: [
            "Content-Type": mime,
            "Content-Length": String(data.count),
            "X-MCPaste-SHA256": digest
        ], body: data)
    }

    static func redirect(to url: URL) -> Self {
        .init(statusCode: 302, headers: ["Location": url.absoluteString], redirectURL: url)
    }

    static func waitingForCancellation() -> Self {
        .init(statusCode: 200, waitsForCancellation: true)
    }
}

private final class RuntimeURLProtocol: URLProtocol, @unchecked Sendable {
    private static let lock = NSLock()
    private static var handler: (@Sendable (URLRequest) throws -> RuntimeStubResponse)?
    private static var stops = 0
    private let stateLock = NSLock()
    private var stopped = false

    static func setHandler(_ handler: @escaping @Sendable (URLRequest) throws -> RuntimeStubResponse) {
        lock.lock()
        self.handler = handler
        lock.unlock()
    }

    static func reset() {
        lock.lock()
        handler = nil
        stops = 0
        lock.unlock()
    }

    static var stopCount: Int {
        lock.lock(); defer { lock.unlock() }
        return stops
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.lock.lock()
        let handler = Self.handler
        Self.lock.unlock()
        DispatchQueue.global().async { [self] in
            do {
                let response = try XCTUnwrap(handler)(request)
                let http = HTTPURLResponse(
                    url: response.responseURL ?? request.url!,
                    statusCode: response.statusCode,
                    httpVersion: nil,
                    headerFields: response.headers
                )!
                if let redirectURL = response.redirectURL {
                    client?.urlProtocol(
                        self,
                        wasRedirectedTo: URLRequest(url: redirectURL),
                        redirectResponse: http
                    )
                    client?.urlProtocolDidFinishLoading(self)
                    return
                }
                if response.waitsForCancellation { return }
                guard !isStopped else { return }
                client?.urlProtocol(self, didReceive: http, cacheStoragePolicy: .notAllowed)
                guard !isStopped else { return }
                client?.urlProtocol(self, didLoad: response.body)
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

private final class LockedRequests: @unchecked Sendable {
    private let lock = NSLock()
    private var requests: [URLRequest] = []

    func append(_ request: URLRequest) {
        var captured = request
        if captured.httpBody == nil, let stream = captured.httpBodyStream {
            captured.httpBody = Self.read(stream)
        }
        lock.lock()
        requests.append(captured)
        lock.unlock()
    }

    private static func read(_ stream: InputStream) -> Data? {
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4 * 1024)
        while true {
            let count = stream.read(&buffer, maxLength: buffer.count)
            if count < 0 { return nil }
            if count == 0 { return data }
            data.append(contentsOf: buffer[0..<count])
        }
    }

    var values: [URLRequest] {
        lock.lock()
        defer { lock.unlock() }
        return requests
    }

    var paths: [String] { values.compactMap(\.url?.path) }
    var urls: [URL] { values.compactMap(\.url) }
}

private final class LockedConcurrency: @unchecked Sendable {
    private let lock = NSLock()
    private var active = 0
    private var peak = 0

    func enter() {
        lock.lock()
        active += 1
        peak = max(peak, active)
        lock.unlock()
    }

    func leave() {
        lock.lock()
        active -= 1
        lock.unlock()
    }

    var maximum: Int {
        lock.lock()
        defer { lock.unlock() }
        return peak
    }
}
