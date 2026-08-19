import AppKit
import XCTest
import MCPasteCore
@testable import MCPasteApp

private struct Publication: Equatable {
    let text: String
    let images: [NormalizedImage]
    let expectedRevision: RuntimeRevision?
}

private actor RuntimeSpy: PeerRuntimeServing {
    private var publishResults: [Result<PublicationResult, PeerRuntimeError>] = []
    private var currentResults: [Result<RuntimeContext?, PeerRuntimeError>] = []
    private var deviceResults: [Result<[PeerDevice], PeerRuntimeError>] = []
    private var publications: [Publication] = []
    private var currentCalls = 0
    private var deviceCalls = 0

    func publish(
        text: String,
        images: [NormalizedImage],
        expectedRevision: RuntimeRevision?
    ) async throws -> PublicationResult {
        let result: PublicationResult
        if !publishResults.isEmpty {
            result = try publishResults.removeFirst().get()
        } else {
            result = PublicationResult(
                revision: RuntimeRevision(
                    wallMillis: (expectedRevision?.wallMillis ?? 0) + 1,
                    logical: 0,
                    deviceID: "local"
                ),
                syncState: .upToDate
            )
        }
        publications.append(Publication(text: text, images: images, expectedRevision: expectedRevision))
        return result
    }

    func current() async throws -> RuntimeContext? {
        currentCalls += 1
        guard !currentResults.isEmpty else { return nil }
        return try currentResults.removeFirst().get()
    }

    func devices() async throws -> [PeerDevice] {
        deviceCalls += 1
        guard !deviceResults.isEmpty else { return [] }
        return try deviceResults.removeFirst().get()
    }

    func enqueuePublish(_ result: Result<PublicationResult, PeerRuntimeError>) {
        publishResults.append(result)
    }

    func enqueueCurrent(_ result: Result<RuntimeContext?, PeerRuntimeError>) {
        currentResults.append(result)
    }

    func enqueueDevices(_ result: Result<[PeerDevice], PeerRuntimeError>) {
        deviceResults.append(result)
    }

    func publicationSnapshot() -> [Publication] { publications }
    func refreshCallCounts() -> (current: Int, devices: Int) { (currentCalls, deviceCalls) }
}

private actor ControlledCurrentRuntime: PeerRuntimeServing {
    private var nextRequest = 0
    private var continuations: [Int: CheckedContinuation<RuntimeContext?, Error>] = [:]
    private let listedDevices: [PeerDevice]

    init(devices: [PeerDevice]) {
        listedDevices = devices
    }

    func publish(
        text: String,
        images: [NormalizedImage],
        expectedRevision: RuntimeRevision?
    ) async throws -> PublicationResult {
        PublicationResult(
            revision: RuntimeRevision(wallMillis: 1, logical: 0, deviceID: "local"),
            syncState: .upToDate
        )
    }

    func current() async throws -> RuntimeContext? {
        let request = nextRequest
        nextRequest += 1
        return try await withCheckedThrowingContinuation { continuation in
            continuations[request] = continuation
        }
    }

    func devices() async throws -> [PeerDevice] { listedDevices }

    func requestCount() -> Int { nextRequest }

    func resume(request: Int, with context: RuntimeContext?) {
        continuations.removeValue(forKey: request)?.resume(returning: context)
    }
}

private actor SuspendedCASRuntime: PeerRuntimeServing {
    private var context: RuntimeContext
    private var expectedRevision: RuntimeRevision?
    private var release: CheckedContinuation<Void, Never>?

    init(context: RuntimeContext) {
        self.context = context
    }

    func publish(
        text: String,
        images: [NormalizedImage],
        expectedRevision: RuntimeRevision?
    ) async throws -> PublicationResult {
        self.expectedRevision = expectedRevision
        await withCheckedContinuation { release = $0 }
        guard expectedRevision == context.revision else { throw PeerRuntimeError.conflict }
        let revision = RuntimeRevision(
            wallMillis: context.revision.wallMillis + 1,
            logical: 0,
            deviceID: "local"
        )
        context = RuntimeContext(
            revision: revision,
            sourceDeviceID: "local",
            updatedAt: Date(timeIntervalSince1970: TimeInterval(revision.wallMillis)),
            text: text,
            assets: [],
            sourceReachable: true,
            syncState: .upToDate
        )
        return PublicationResult(revision: revision, syncState: .upToDate)
    }

    func current() async throws -> RuntimeContext? { context }
    func devices() async throws -> [PeerDevice] { [] }
    func capturedExpectedRevision() -> RuntimeRevision? { expectedRevision }
    func currentText() -> String { context.text }

    func installRemote(_ remote: RuntimeContext) {
        context = remote
    }

    func resumePublish() {
        release?.resume()
        release = nil
    }
}

private actor SuspendedLocalIntentRuntime: PeerRuntimeServing {
    private var context: RuntimeContext
    private var publications: [Publication] = []
    private var firstRelease: CheckedContinuation<Void, Never>?
    private let firstError: PeerRuntimeError?

    init(context: RuntimeContext, firstError: PeerRuntimeError? = nil) {
        self.context = context
        self.firstError = firstError
    }

    func publish(
        text: String,
        images: [NormalizedImage],
        expectedRevision: RuntimeRevision?
    ) async throws -> PublicationResult {
        publications.append(Publication(text: text, images: images, expectedRevision: expectedRevision))
        let isFirst = publications.count == 1
        if isFirst {
            await withCheckedContinuation { firstRelease = $0 }
        }
        if isFirst, let firstError { throw firstError }
        guard expectedRevision == context.revision else { throw PeerRuntimeError.conflict }
        let revision = RuntimeRevision(
            wallMillis: context.revision.wallMillis + 1,
            logical: 0,
            deviceID: "local"
        )
        context = RuntimeContext(
            revision: revision,
            sourceDeviceID: "local",
            updatedAt: Date(timeIntervalSince1970: TimeInterval(revision.wallMillis)),
            text: text,
            assets: [],
            sourceReachable: true,
            syncState: .upToDate
        )
        return PublicationResult(revision: revision, syncState: .upToDate)
    }

    func current() async throws -> RuntimeContext? { context }
    func devices() async throws -> [PeerDevice] { [] }
    func firstPublishStarted() -> Bool { !publications.isEmpty }
    func publicationSnapshot() -> [Publication] { publications }
    func currentText() -> String { context.text }
    func currentRevision() -> RuntimeRevision { context.revision }

    func resumeFirstPublish() {
        firstRelease?.resume()
        firstRelease = nil
    }
}

private actor CancellableSuspension {
    private var started = false
    private var cancelled = false
    private var cancellationRequested = false
    private var continuation: CheckedContinuation<Void, Error>?

    func wait() async throws {
        started = true
        try await withTaskCancellationHandler(operation: {
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
                if Task.isCancelled || cancellationRequested {
                    cancelled = true
                    continuation.resume(throwing: CancellationError())
                } else {
                    self.continuation = continuation
                }
            }
        }, onCancel: {
            Task { await self.cancel() }
        })
    }

    func hasStarted() -> Bool { started }
    func wasCancelled() -> Bool { cancelled }

    func release() {
        continuation?.resume()
        continuation = nil
    }

    private func cancel() {
        cancellationRequested = true
        cancelled = true
        continuation?.resume(throwing: CancellationError())
        continuation = nil
    }
}

private actor CancellationAwareRuntime: PeerRuntimeServing {
    private let suspension = CancellableSuspension()
    private var committed = false
    private var stopped = false

    func publish(
        text: String,
        images: [NormalizedImage],
        expectedRevision: RuntimeRevision?
    ) async throws -> PublicationResult {
        try await suspension.wait()
        committed = true
        return PublicationResult(
            revision: RuntimeRevision(wallMillis: 1, logical: 0, deviceID: "local"),
            syncState: .upToDate
        )
    }

    func current() async throws -> RuntimeContext? { nil }
    func devices() async throws -> [PeerDevice] { [] }
    func publishStarted() async -> Bool { await suspension.hasStarted() }
    func publishWasCancelled() async -> Bool { await suspension.wasCancelled() }
    func didCommit() -> Bool { committed }
    func didStop() -> Bool { stopped }
    func releasePublish() async { await suspension.release() }
    func markStopped() { stopped = true }
}

private actor ManualDebounceClock: DebounceClock {
    private struct Waiter {
        let nanoseconds: UInt64
        let continuation: CheckedContinuation<Void, Error>
    }

    private var waiters: [UUID: Waiter] = [:]
    private var cancelled: Set<UUID> = []
    private var requestedDurations: [UInt64] = []

    func sleep(nanoseconds: UInt64) async throws {
        let id = UUID()
        try await withTaskCancellationHandler(operation: {
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
                requestedDurations.append(nanoseconds)
                if cancelled.remove(id) != nil {
                    continuation.resume(throwing: CancellationError())
                } else {
                    waiters[id] = Waiter(nanoseconds: nanoseconds, continuation: continuation)
                }
            }
        }, onCancel: {
            Task { await self.cancel(id) }
        })
    }

    func advanceAll() {
        let pending = Array(waiters.values)
        waiters.removeAll()
        pending.forEach { $0.continuation.resume() }
    }

    func pendingCount() -> Int { waiters.count }
    func durations() -> [UInt64] { requestedDurations }

    private func cancel(_ id: UUID) {
        if let waiter = waiters.removeValue(forKey: id) {
            waiter.continuation.resume(throwing: CancellationError())
        } else {
            cancelled.insert(id)
        }
    }
}

private actor NormalizationGate {
    private var inputs: [[Data]] = []
    private var continuations: [Int: CheckedContinuation<[NormalizedImage], Error>] = [:]

    func normalize(_ data: [Data]) async throws -> [NormalizedImage] {
        let index = inputs.count
        inputs.append(data)
        return try await withCheckedThrowingContinuation { continuation in
            continuations[index] = continuation
        }
    }

    func startedInputs() -> [[Data]] { inputs }

    func succeed(_ index: Int, with images: [NormalizedImage]) {
        continuations.removeValue(forKey: index)?.resume(returning: images)
    }
}

private actor LifecycleSpy {
    private var starts = 0
    private var stops = 0
    private let runtime: any PeerRuntimeServing
    private let launchError: PeerRuntimeProcessError?

    init(runtime: any PeerRuntimeServing, launchError: PeerRuntimeProcessError? = nil) {
        self.runtime = runtime
        self.launchError = launchError
    }

    func start() async throws -> any PeerRuntimeServing {
        starts += 1
        if let launchError { throw launchError }
        return runtime
    }

    func stop() async { stops += 1 }
    func counts() -> (starts: Int, stops: Int) { (starts, stops) }
}

private actor StartGate {
    private var started = false
    private var continuation: CheckedContinuation<Void, Never>?

    func wait() async {
        started = true
        await withCheckedContinuation { continuation in
            self.continuation = continuation
        }
    }

    func hasStarted() -> Bool { started }

    func release() {
        continuation?.resume()
        continuation = nil
    }
}

@MainActor
final class AppModelTests: XCTestCase {
    private let second: UInt64 = 1_000_000_000

    func testModelStartsWithOnlyTheCurrentContextState() {
        let model = makeModel()

        XCTAssertEqual(model.draft, "")
        XCTAssertEqual(model.attachments, [])
        XCTAssertEqual(model.devices, [])
        XCTAssertEqual(model.syncState, .waitingToSync)
        XCTAssertNil(model.lastUpdatedBy)
        XCTAssertNil(model.lastUpdatedAt)
        XCTAssertNil(model.errorMessage)
        XCTAssertEqual(model.uploadingCount, 0)
        XCTAssertEqual(model.connectorNames, [])
    }

    func testProductionDefaultRefreshCadenceIsFiveHundredMilliseconds() {
        XCTAssertEqual(AppModel.defaultRefreshIntervalNanoseconds, 500_000_000)
    }

    func testDraftPublishesOneExactTextAndOrderedImageSnapshotAfterOneSecond() async {
        let runtime = RuntimeSpy()
        let clock = ManualDebounceClock()
        let images = [image(1), image(2)]
        let model = makeModel(runtime: runtime, clock: clock)
        await seed(model, runtime: runtime, revision: 1, attachments: images)

        model.draft = "line 1\nline 2   "
        await waitUntil { await clock.pendingCount() == 1 }

        let durations = await clock.durations()
        let publicationsBeforeAdvance = await runtime.publicationSnapshot()
        XCTAssertEqual(durations, [second])
        XCTAssertTrue(publicationsBeforeAdvance.isEmpty)

        await clock.advanceAll()
        await waitUntil { await runtime.publicationSnapshot().count == 1 }

        let publications = await runtime.publicationSnapshot()
        XCTAssertEqual(publications, [Publication(
            text: "line 1\nline 2   ",
            images: images,
            expectedRevision: revision(1)
        )])
        XCTAssertEqual(model.syncState, .upToDate)
    }

    func testRapidDraftEditsCancelTheStaleSnapshot() async {
        let runtime = RuntimeSpy()
        let clock = ManualDebounceClock()
        let model = makeModel(runtime: runtime, clock: clock)

        model.draft = "stale"
        await waitUntil { await clock.pendingCount() == 1 }
        model.draft = "latest exact text"
        await waitUntil {
            let durations = await clock.durations()
            let pending = await clock.pendingCount()
            return durations.count == 2 && pending == 1
        }
        await clock.advanceAll()
        await waitUntil { await runtime.publicationSnapshot().count == 1 }

        let publications = await runtime.publicationSnapshot()
        XCTAssertEqual(publications.map(\.text), ["latest exact text"])
    }

    func testAttachmentNormalizationAndPublicationAreSerialized() async {
        let runtime = RuntimeSpy()
        let gate = NormalizationGate()
        let model = makeModel(runtime: runtime) { try await gate.normalize($0) }

        let first = Task { await model.uploadImages([Data([1])]) }
        await waitUntil { await gate.startedInputs().count == 1 }
        let second = Task { await model.uploadImages([Data([2])]) }
        await Task.yield()

        let startedBeforeFirstCompletion = await gate.startedInputs()
        XCTAssertEqual(startedBeforeFirstCompletion, [[Data([1])]])

        await gate.succeed(0, with: [image(1)])
        await waitUntil { await gate.startedInputs().count == 2 }
        await gate.succeed(1, with: [image(2)])
        await first.value
        await second.value

        let publications = await runtime.publicationSnapshot()
        XCTAssertEqual(
            publications,
            [
                Publication(text: "", images: [image(1)], expectedRevision: nil),
                Publication(
                    text: "",
                    images: [image(1), image(2)],
                    expectedRevision: RuntimeRevision(wallMillis: 1, logical: 0, deviceID: "local")
                )
            ]
        )
        XCTAssertEqual(model.attachments, [image(1), image(2)])
        XCTAssertEqual(model.uploadingCount, 0)
    }

    func testConcurrentOriginalIndexRemovalsDeleteBothSelectedAttachments() async {
        let firstImage = image(1)
        let secondImage = image(2)
        let base = context(
            1,
            source: "local",
            text: "base",
            assets: [runtimeAsset(firstImage, index: 0), runtimeAsset(secondImage, index: 1)]
        )
        let runtime = SuspendedLocalIntentRuntime(context: base)
        let model = makeModel(runtime: runtime)
        await model.refreshNow()
        let firstRemoval = Task { await model.removeAttachment(at: 0) }
        await waitUntil { await runtime.firstPublishStarted() }

        let secondRemoval = Task { await model.removeAttachment(at: 1) }
        for _ in 0..<20 { await Task.yield() }
        await runtime.resumeFirstPublish()
        await firstRemoval.value
        await secondRemoval.value

        let publications = await runtime.publicationSnapshot()
        XCTAssertEqual(publications.map(\.images), [[secondImage], []])
        XCTAssertEqual(model.attachments, [])
        XCTAssertNil(model.errorMessage)
    }

    func testConcurrentOriginalIndexRemovalsDistinguishEqualDuplicateAttachments() async {
        let duplicate = image(3)
        let base = context(
            1,
            source: "local",
            text: "base",
            assets: [runtimeAsset(duplicate, index: 0), runtimeAsset(duplicate, index: 1)]
        )
        let runtime = SuspendedLocalIntentRuntime(context: base)
        let model = makeModel(runtime: runtime)
        await model.refreshNow()
        let firstRemoval = Task { await model.removeAttachment(at: 0) }
        await waitUntil { await runtime.firstPublishStarted() }

        let secondRemoval = Task { await model.removeAttachment(at: 1) }
        for _ in 0..<20 { await Task.yield() }
        await runtime.resumeFirstPublish()
        await firstRemoval.value
        await secondRemoval.value

        let publications = await runtime.publicationSnapshot()
        XCTAssertEqual(publications.map(\.images), [[duplicate], []])
        XCTAssertEqual(model.attachments, [])
        XCTAssertNil(model.errorMessage)
    }

    func testNormalizationFailurePreservesThePriorPublishedSnapshot() async {
        let runtime = RuntimeSpy()
        let prior = [image(9)]
        let model = makeModel(runtime: runtime) { _ in throw ImageNormalizationError.animated }
        await seed(model, runtime: runtime, revision: 1, draft: "prior", attachments: prior)

        await model.uploadImages([Data([7])])

        XCTAssertEqual(model.draft, "prior")
        XCTAssertEqual(model.attachments, prior)
        XCTAssertEqual(model.errorMessage, "Animated images aren’t supported.")
        let publications = await runtime.publicationSnapshot()
        XCTAssertTrue(publications.isEmpty)
    }

    func testImagePublicationFailureDoesNotExposeAnUnpublishedAttachment() async {
        let runtime = RuntimeSpy()
        let model = makeModel(runtime: runtime) { _ in [self.image(3)] }
        await seed(model, runtime: runtime, revision: 1, draft: "kept", attachments: [image(1)])
        await runtime.enqueuePublish(.failure(.unavailable))

        await model.uploadImages([Data([3])])

        XCTAssertEqual(model.attachments, [image(1)])
        XCTAssertEqual(model.syncState, .waitingToSync)
        XCTAssertEqual(model.errorMessage, "Changes couldn’t be published. Check that MCPaste is still running.")
        let publications = await runtime.publicationSnapshot()
        XCTAssertTrue(publications.isEmpty)
    }

    func testClearPublishesAndThenShowsTheExactEmptySnapshot() async {
        let runtime = RuntimeSpy()
        let model = makeModel(runtime: runtime)
        await seed(model, runtime: runtime, revision: 1, draft: "remove", attachments: [image(1)])

        await model.clearContext()

        let publications = await runtime.publicationSnapshot()
        XCTAssertEqual(publications, [Publication(text: "", images: [], expectedRevision: revision(1))])
        XCTAssertEqual(model.draft, "")
        XCTAssertEqual(model.attachments, [])
        XCTAssertEqual(model.syncState, .upToDate)
    }

    func testUnavailableDraftPublicationMapsToWaitingWithoutClaimingItWasPublished() async {
        let runtime = RuntimeSpy()
        await runtime.enqueuePublish(.failure(.unavailable))
        let clock = ManualDebounceClock()
        let model = makeModel(runtime: runtime, clock: clock)

        model.draft = "local edit"
        await waitUntil { await clock.pendingCount() == 1 }
        await clock.advanceAll()
        await waitUntil { model.errorMessage != nil }

        XCTAssertEqual(model.syncState, .waitingToSync)
        XCTAssertEqual(model.errorMessage, "Changes couldn’t be published. Check that MCPaste is still running.")
        let publications = await runtime.publicationSnapshot()
        XCTAssertTrue(publications.isEmpty)
    }

    func testCurrentDraftFailureStopsWithoutAutomaticRetry() async {
        let runtime = RuntimeSpy()
        await runtime.enqueuePublish(.failure(.unavailable))
        let clock = ManualDebounceClock()
        let model = makeModel(runtime: runtime, clock: clock)
        model.draft = "failed current intent"
        await waitUntil { await clock.pendingCount() == 1 }

        await clock.advanceAll()
        await waitUntil { model.errorMessage != nil }
        for _ in 0..<20 { await Task.yield() }

        let durations = await clock.durations()
        let pending = await clock.pendingCount()
        XCTAssertEqual(durations, [second])
        XCTAssertEqual(pending, 0)
        XCTAssertEqual(model.syncState, .waitingToSync)
        XCTAssertEqual(model.errorMessage, "Changes couldn’t be published. Check that MCPaste is still running.")
    }

    func testFailedDraftIgnoresSameRevisionRefreshAndStillDoesNotRetry() async {
        let runtime = RuntimeSpy()
        let clock = ManualDebounceClock()
        let model = makeModel(runtime: runtime, clock: clock)
        let base = context(1, source: "local", text: "published base")
        await runtime.enqueueCurrent(.success(base))
        await model.refreshNow()
        await runtime.enqueuePublish(.failure(.unavailable))
        model.draft = "failed local edit"
        await waitUntil { await clock.pendingCount() == 1 }
        await clock.advanceAll()
        await waitUntil { model.errorMessage != nil }
        await runtime.enqueueCurrent(.success(base))

        await model.refreshNow()
        for _ in 0..<20 { await Task.yield() }

        XCTAssertEqual(model.draft, "failed local edit")
        XCTAssertEqual(model.syncState, .waitingToSync)
        XCTAssertEqual(model.errorMessage, "Changes couldn’t be published. Check that MCPaste is still running.")
        let durations = await clock.durations()
        let pending = await clock.pendingCount()
        XCTAssertEqual(durations, [second])
        XCTAssertEqual(pending, 0)
    }

    func testNewLocalEditMovesFailedDraftBackToPending() async {
        let runtime = RuntimeSpy()
        let clock = ManualDebounceClock()
        let model = makeModel(runtime: runtime, clock: clock)
        await runtime.enqueuePublish(.failure(.unavailable))
        model.draft = "failed local edit"
        await waitUntil { await clock.pendingCount() == 1 }
        await clock.advanceAll()
        await waitUntil { model.errorMessage != nil }

        model.draft = "new local intent"
        await waitUntil { await clock.pendingCount() == 1 }

        XCTAssertEqual(model.syncState, .updating)
        XCTAssertNil(model.errorMessage)
        let durations = await clock.durations()
        XCTAssertEqual(durations, [second, second])
    }

    func testHigherRemoteRevisionReplacesFailedDraftAndClearsFailureState() async {
        let runtime = RuntimeSpy()
        let clock = ManualDebounceClock()
        let model = makeModel(runtime: runtime, clock: clock)
        await seed(model, runtime: runtime, revision: 1, draft: "published base")
        await runtime.enqueuePublish(.failure(.unavailable))
        model.draft = "failed local edit"
        await waitUntil { await clock.pendingCount() == 1 }
        await clock.advanceAll()
        await waitUntil { model.errorMessage != nil }
        await runtime.enqueueCurrent(.success(context(2, source: "remote", text: "new remote winner")))

        await model.refreshNow()

        XCTAssertEqual(model.draft, "new remote winner")
        XCTAssertEqual(model.syncState, .upToDate)
        XCTAssertNil(model.errorMessage)
        let pending = await clock.pendingCount()
        XCTAssertEqual(pending, 0)
    }

    func testSuccessfulPublicationImmediatelyAppliesReturnedSyncStateAndRevision() async {
        for state in [PeerSyncState.upToDate, .updating, .waitingToSync] {
            let runtime = RuntimeSpy()
            let committed = RuntimeRevision(wallMillis: 50, logical: 2, deviceID: "local")
            await runtime.enqueuePublish(.success(PublicationResult(revision: committed, syncState: state)))
            let clock = ManualDebounceClock()
            let model = makeModel(runtime: runtime, clock: clock)
            model.draft = "state \(state.rawValue)"
            await waitUntil { await clock.pendingCount() == 1 }

            await clock.advanceAll()
            await waitUntil { await runtime.publicationSnapshot().count == 1 }

            XCTAssertEqual(model.syncState, state)
            model.draft = "next"
            await waitUntil { await clock.pendingCount() == 1 }
            await clock.advanceAll()
            await waitUntil { await runtime.publicationSnapshot().count == 2 }
            let publications = await runtime.publicationSnapshot()
            XCTAssertEqual(publications[1].expectedRevision, committed)
        }
    }

    func testHigherRemoteRevisionReplacesTextAndAssetsAtomicallyAndMapsSourceMetadata() async {
        let runtime = RuntimeSpy()
        let sourceTime = Date(timeIntervalSince1970: 1_700_000_000)
        let devices = fixtureDevices(now: sourceTime)
        let assets = [asset(1, mime: "image/png", width: 11, height: 12), asset(2, mime: "image/jpeg", width: 21, height: 22)]
        let model = makeModel(runtime: runtime)
        await seed(model, runtime: runtime, revision: 1, draft: "old", attachments: [image(9)])
        await runtime.enqueueDevices(.success(devices))
        await runtime.enqueueCurrent(.success(context(2, source: "remote", date: sourceTime, text: "remote exact", assets: assets)))

        await model.refreshNow()

        XCTAssertEqual(model.draft, "remote exact")
        XCTAssertEqual(
            model.attachments,
            [
                NormalizedImage(mimeType: "image/png", width: 11, height: 12, data: Data([1, 11])),
                NormalizedImage(mimeType: "image/jpeg", width: 21, height: 22, data: Data([2, 12]))
            ]
        )
        XCTAssertEqual(model.devices, devices)
        XCTAssertEqual(model.lastUpdatedBy, "Studio Mac")
        XCTAssertEqual(model.lastUpdatedAt, sourceTime)
        XCTAssertEqual(model.syncState, .upToDate)
    }

    func testUnchangedRevisionDoesNotRewriteAnInProgressDraft() async {
        let runtime = RuntimeSpy()
        let clock = ManualDebounceClock()
        let remote = context(2, source: "remote", text: "stored")
        await runtime.enqueueDevices(.success(fixtureDevices()))
        await runtime.enqueueCurrent(.success(remote))
        let model = makeModel(runtime: runtime, clock: clock)
        await model.refreshNow()
        model.draft = "typing locally"
        await waitUntil { await clock.pendingCount() == 1 }
        await runtime.enqueueDevices(.success(fixtureDevices()))
        await runtime.enqueueCurrent(.success(remote))

        await model.refreshNow()

        XCTAssertEqual(model.draft, "typing locally")
        let pending = await clock.pendingCount()
        XCTAssertEqual(pending, 1)
    }

    func testOutOfOrderRefreshCompletionCannotRegressTheAppliedRevision() async {
        let devices = fixtureDevices()
        let runtime = ControlledCurrentRuntime(devices: devices)
        let model = makeModel(runtime: runtime)
        let older = context(2, source: "remote", text: "older")
        let newer = context(3, source: "remote", text: "newer")

        let first = Task { await model.refreshNow() }
        await waitUntil { await runtime.requestCount() == 1 }
        let second = Task { await model.refreshNow() }
        await waitUntil { await runtime.requestCount() == 2 }
        await runtime.resume(request: 1, with: newer)
        await waitUntil { model.draft == "newer" }
        await runtime.resume(request: 0, with: older)
        await first.value
        await second.value

        XCTAssertEqual(model.draft, "newer")
        XCTAssertEqual(model.lastUpdatedAt, newer.updatedAt)
    }

    func testHigherRemoteRevisionCancelsAQueuedOlderLocalEdit() async {
        let runtime = RuntimeSpy()
        let clock = ManualDebounceClock()
        let model = makeModel(runtime: runtime, clock: clock)
        await seed(model, runtime: runtime, revision: 1, draft: "base")
        model.draft = "queued local"
        await waitUntil { await clock.pendingCount() == 1 }
        await runtime.enqueueDevices(.success(fixtureDevices()))
        await runtime.enqueueCurrent(.success(context(2, source: "remote", text: "winner")))

        await model.refreshNow()
        await clock.advanceAll()
        await Task.yield()

        XCTAssertEqual(model.draft, "winner")
        let publications = await runtime.publicationSnapshot()
        let pending = await clock.pendingCount()
        XCTAssertTrue(publications.isEmpty)
        XCTAssertEqual(pending, 0)
    }

    func testHigherRemoteRevisionRejectsAnAlreadyInFlightOlderLocalPublish() async {
        let base = context(1, source: "local", text: "base")
        let winner = context(2, source: "remote", text: "remote winner")
        let runtime = SuspendedCASRuntime(context: base)
        let clock = ManualDebounceClock()
        let model = makeModel(runtime: runtime, clock: clock)
        await model.refreshNow()
        model.draft = "stale local"
        await waitUntil { await clock.pendingCount() == 1 }

        await clock.advanceAll()
        await waitUntil { await runtime.capturedExpectedRevision() != nil }
        let capturedExpectedRevision = await runtime.capturedExpectedRevision()
        XCTAssertEqual(capturedExpectedRevision, base.revision)
        await runtime.installRemote(winner)
        await model.refreshNow()
        await runtime.resumePublish()
        await waitUntil { model.draft == "remote winner" }

        let runtimeText = await runtime.currentText()
        XCTAssertEqual(runtimeText, "remote winner")
        XCTAssertEqual(model.syncState, .upToDate)
        XCTAssertNil(model.errorMessage)
    }

    func testNewerDraftRebasesAfterAnAlreadyStartedOlderPublishAndWins() async {
        let base = context(1, source: "local", text: "base")
        let runtime = SuspendedLocalIntentRuntime(context: base)
        let clock = ManualDebounceClock()
        let model = makeModel(runtime: runtime, clock: clock)
        await model.refreshNow()
        model.draft = "older local text"
        await waitUntil { await clock.pendingCount() == 1 }
        await clock.advanceAll()
        await waitUntil { await runtime.firstPublishStarted() }

        model.draft = "latest exact text"
        await runtime.resumeFirstPublish()
        await waitUntil { await runtime.currentText() == "older local text" }
        let firstCommittedRevision = await runtime.currentRevision()
        await waitUntil { await clock.pendingCount() == 1 }
        await clock.advanceAll()
        await waitUntil { await runtime.currentText() == "latest exact text" }

        let publications = await runtime.publicationSnapshot()
        XCTAssertEqual(publications.map(\.text), ["older local text", "latest exact text"])
        XCTAssertEqual(publications.map(\.expectedRevision), [base.revision, firstCommittedRevision])
        XCTAssertEqual(model.draft, "latest exact text")
        XCTAssertEqual(model.syncState, .upToDate)
        XCTAssertNil(model.errorMessage)
    }

    func testNewerDraftReschedulesAfterAnOlderStartedPublishFails() async {
        let base = context(1, source: "local", text: "base")
        let runtime = SuspendedLocalIntentRuntime(context: base, firstError: .unavailable)
        let clock = ManualDebounceClock()
        let model = makeModel(runtime: runtime, clock: clock)
        await model.refreshNow()
        model.draft = "older failing text"
        await waitUntil { await clock.pendingCount() == 1 }
        await clock.advanceAll()
        await waitUntil { await runtime.firstPublishStarted() }

        model.draft = "latest after failure"
        await runtime.resumeFirstPublish()
        await waitUntil { await clock.pendingCount() == 1 }
        await clock.advanceAll()
        await waitUntil { await runtime.currentText() == "latest after failure" }

        let publications = await runtime.publicationSnapshot()
        XCTAssertEqual(publications.map(\.text), ["older failing text", "latest after failure"])
        XCTAssertEqual(publications.map(\.expectedRevision), [base.revision, base.revision])
        XCTAssertEqual(model.syncState, .upToDate)
        XCTAssertNil(model.errorMessage)
    }

    func testAttachmentPublicationWaitsForStartedDraftAndUsesItsCommittedRevision() async {
        let base = context(1, source: "local", text: "base")
        let runtime = SuspendedLocalIntentRuntime(context: base)
        let clock = ManualDebounceClock()
        let uploaded = image(7)
        let model = makeModel(runtime: runtime, clock: clock) { _ in [uploaded] }
        await model.refreshNow()
        model.draft = "draft in flight"
        await waitUntil { await clock.pendingCount() == 1 }
        await clock.advanceAll()
        await waitUntil { await runtime.firstPublishStarted() }

        let upload = Task { await model.uploadImages([Data([7])]) }
        await waitUntil { model.uploadingCount == 1 }
        await runtime.resumeFirstPublish()
        await upload.value
        await waitUntil { await runtime.publicationSnapshot().count == 2 }

        let firstCommittedRevision = RuntimeRevision(wallMillis: 2, logical: 0, deviceID: "local")
        let publications = await runtime.publicationSnapshot()
        XCTAssertEqual(publications.map(\.expectedRevision), [base.revision, firstCommittedRevision])
        XCTAssertEqual(publications.map(\.images), [[], [uploaded]])
        XCTAssertEqual(model.attachments, [uploaded])
        XCTAssertEqual(model.syncState, .upToDate)
        XCTAssertNil(model.errorMessage)
    }

    func testNewerDraftAfterStartedClearRebasesOnTheClearCommitAndWins() async {
        let base = context(1, source: "local", text: "base")
        let runtime = SuspendedLocalIntentRuntime(context: base)
        let clock = ManualDebounceClock()
        let model = makeModel(runtime: runtime, clock: clock)
        await model.refreshNow()
        let clear = Task { await model.clearContext() }
        await waitUntil { await runtime.firstPublishStarted() }

        model.draft = "latest after clear"
        await runtime.resumeFirstPublish()
        await clear.value
        XCTAssertEqual(model.draft, "latest after clear")
        await waitUntil { await clock.pendingCount() == 1 }
        await clock.advanceAll()
        await waitUntil { await runtime.currentText() == "latest after clear" }

        let clearRevision = RuntimeRevision(wallMillis: 2, logical: 0, deviceID: "local")
        let publications = await runtime.publicationSnapshot()
        XCTAssertEqual(publications.map(\.text), ["", "latest after clear"])
        XCTAssertEqual(publications.map(\.expectedRevision), [base.revision, clearRevision])
        XCTAssertEqual(model.syncState, .upToDate)
        XCTAssertNil(model.errorMessage)
    }

    func testFirstObservedSourceOfflineReplicaIsVisibleWithOrderedAssets() async {
        let runtime = RuntimeSpy()
        let model = makeModel(runtime: runtime)
        let assets = [
            asset(1, mime: "image/png", width: 11, height: 12),
            asset(2, mime: "image/jpeg", width: 21, height: 22)
        ]
        await runtime.enqueueDevices(.success(fixtureDevices()))
        await runtime.enqueueCurrent(.success(context(
            4,
            source: "remote",
            text: "offline replica",
            assets: assets,
            reachable: false,
            state: .sourceOffline
        )))

        await model.refreshNow()

        XCTAssertEqual(model.draft, "offline replica")
        XCTAssertEqual(model.attachments, [
            NormalizedImage(mimeType: "image/png", width: 11, height: 12, data: Data([1, 11])),
            NormalizedImage(mimeType: "image/jpeg", width: 21, height: 22, data: Data([2, 12]))
        ])
        XCTAssertEqual(model.lastUpdatedBy, "Studio Mac")
        XCTAssertEqual(model.syncState, .sourceOffline)
        XCTAssertNil(model.errorMessage)
    }

    func testSourceOfflineRefreshKeepsTheVisibleReplica() async {
        let runtime = RuntimeSpy()
        let model = makeModel(runtime: runtime)
        await seed(model, runtime: runtime, revision: 4, draft: "visible replica", attachments: [image(4)])
        await runtime.enqueueDevices(.success(fixtureDevices()))
        await runtime.enqueueCurrent(.failure(.sourceOffline))

        await model.refreshNow()

        XCTAssertEqual(model.draft, "visible replica")
        XCTAssertEqual(model.attachments, [image(4)])
        XCTAssertEqual(model.syncState, .sourceOffline)
    }

    func testRefreshLifecycleRunsAndCancellationStopsFurtherCalls() async {
        let runtime = RuntimeSpy()
        let model = makeModel(runtime: runtime, refreshInterval: 5_000_000)

        model.activate()
        await waitUntil { await runtime.refreshCallCounts().current >= 2 }
        await model.deactivate()
        let stoppedAt = await runtime.refreshCallCounts()
        try? await Task.sleep(nanoseconds: 30_000_000)

        let afterWait = await runtime.refreshCallCounts()
        XCTAssertEqual(afterWait.current, stoppedAt.current)
        XCTAssertEqual(afterWait.devices, stoppedAt.devices)
    }

    func testDeactivationCancelsQueuedDraftPublication() async {
        let runtime = RuntimeSpy()
        let clock = ManualDebounceClock()
        let model = makeModel(runtime: runtime, clock: clock)

        model.draft = "must not publish"
        await waitUntil { await clock.pendingCount() == 1 }
        await model.deactivate()
        await waitUntil { await clock.pendingCount() == 0 }
        await clock.advanceAll()
        await Task.yield()

        let publications = await runtime.publicationSnapshot()
        XCTAssertTrue(publications.isEmpty)
    }

    func testSerialGatePropagatesCallerCancellationToCurrentWork() async {
        let gate = SerialGate()
        let suspension = CancellableSuspension()
        let caller = Task {
            await gate.run {
                try? await suspension.wait()
            }
        }
        await waitUntil { await suspension.hasStarted() }

        caller.cancel()
        await waitUntil(timeoutNanoseconds: 100_000_000) { await suspension.wasCancelled() }
        await suspension.release()
        await caller.value

        let cancelled = await suspension.wasCancelled()
        XCTAssertTrue(cancelled)
    }

    func testLifecycleTerminationAwaitsInFlightPublicationCancellationWithoutCommitOrRetention() async throws {
        var runtime: CancellationAwareRuntime? = CancellationAwareRuntime()
        weak var weakRuntime = runtime
        let clock = ManualDebounceClock()
        var model: AppModel? = makeModel(clock: clock)
        weak var weakModel = model
        let lifecycle = AppLifecycleController(runtimeOwner: RuntimeOwner(
            start: { [weak runtime] in
                guard let runtime else { throw PeerRuntimeProcessError.launchFailed }
                return runtime
            },
            stop: { [weak runtime] in await runtime?.markStopped() }
        ))
        await lifecycle.launch(model: try XCTUnwrap(model)) {}
        model?.draft = "must never commit"
        await waitUntil { await clock.pendingCount() == 1 }
        await clock.advanceAll()
        await waitUntil { await runtime?.publishStarted() == true }

        await lifecycle.terminate(model: try XCTUnwrap(model))
        let cancelled = await runtime?.publishWasCancelled()
        let stopped = await runtime?.didStop()
        await runtime?.releasePublish()
        for _ in 0..<20 { await Task.yield() }

        let committed = await runtime?.didCommit()
        XCTAssertEqual(cancelled, true)
        XCTAssertEqual(stopped, true)
        XCTAssertEqual(committed, false)
        XCTAssertEqual(model?.draft, "must never commit")
        XCTAssertEqual(model?.attachments, [])
        XCTAssertNil(model?.errorMessage)

        model = nil
        runtime = nil
        for _ in 0..<20 { await Task.yield() }
        XCTAssertNil(weakModel)
        XCTAssertNil(weakRuntime)
    }

    func testInstallingRuntimeIsSingleFlightAndStoresConnectorConfigurationNames() async {
        let first = RuntimeSpy()
        let secondRuntime = RuntimeSpy()
        let connectorCalls = Counter()
        let model = AppModel(
            debounceClock: ManualDebounceClock(),
            refreshIntervalNanoseconds: second,
            automaticallyRefresh: false,
            normalize: { _ in [] },
            configureConnectors: {
                await connectorCalls.increment()
                return ["Codex", "Claude Code"]
            }
        )

        XCTAssertTrue(model.installRuntime(first))
        XCTAssertFalse(model.installRuntime(secondRuntime))
        await waitUntil { model.connectorNames == ["Codex", "Claude Code"] }

        let connectorCallCount = await connectorCalls.value()
        XCTAssertEqual(connectorCallCount, 1)
    }

    func testConnectorNamesSurviveARefreshThatFinishesBeforeRegistration() async {
        let runtime = RuntimeSpy()
        let connectorGate = StartGate()
        let model = AppModel(
            debounceClock: ManualDebounceClock(),
            refreshIntervalNanoseconds: second,
            automaticallyRefresh: false,
            normalize: { _ in [] },
            configureConnectors: {
                await connectorGate.wait()
                return ["Codex", "Claude Code"]
            }
        )
        XCTAssertTrue(model.installRuntime(runtime))
        await waitUntil { await connectorGate.hasStarted() }
        await runtime.enqueueCurrent(.success(context(1, source: "local", text: "ready")))

        await model.refreshNow()
        await connectorGate.release()
        await waitUntil { model.connectorNames == ["Codex", "Claude Code"] }

        XCTAssertEqual(model.connectorNames, ["Codex", "Claude Code"])
    }

    func testRuntimeLaunchFailureLeavesWaitingStateWithFixedActionableError() {
        let model = makeModel()

        model.reportRuntimeLaunchFailure()

        XCTAssertEqual(model.syncState, .waitingToSync)
        XCTAssertEqual(model.errorMessage, "MCPaste couldn’t start its local helper. Quit and reopen MCPaste.")
    }

    func testLifecycleAlwaysOpensStartsOnceAndStopsWithoutPublishingOrSavingContent() async {
        let runtime = RuntimeSpy()
        let lifecycleSpy = LifecycleSpy(runtime: runtime)
        let owner = RuntimeOwner(
            start: { try await lifecycleSpy.start() },
            stop: { await lifecycleSpy.stop() }
        )
        let clock = ManualDebounceClock()
        let model = AppModel(
            debounceClock: clock,
            refreshIntervalNanoseconds: second,
            automaticallyRefresh: false,
            normalize: { _ in [] },
            configureConnectors: { [] }
        )
        let lifecycle = AppLifecycleController(runtimeOwner: owner)
        var openCount = 0

        await lifecycle.launch(model: model) { openCount += 1 }
        await lifecycle.launch(model: model) { openCount += 1 }
        model.draft = "ephemeral content"
        await waitUntil { await clock.pendingCount() == 1 }
        await lifecycle.terminate(model: model)
        await clock.advanceAll()
        await Task.yield()

        XCTAssertEqual(openCount, 2, "opening the editor is unconditional even after launch was attempted")
        let lifecycleCounts = await lifecycleSpy.counts()
        let publications = await runtime.publicationSnapshot()
        XCTAssertEqual(lifecycleCounts.starts, 1)
        XCTAssertEqual(lifecycleCounts.stops, 1)
        XCTAssertTrue(publications.isEmpty, "termination must not publish or save queued content")
        XCTAssertEqual(model.draft, "ephemeral content")
    }

    func testLifecycleLaunchFailureStillOpensTheEditor() async {
        let runtime = RuntimeSpy()
        let lifecycleSpy = LifecycleSpy(runtime: runtime, launchError: .launchFailed)
        let lifecycle = AppLifecycleController(runtimeOwner: RuntimeOwner(
            start: { try await lifecycleSpy.start() },
            stop: { await lifecycleSpy.stop() }
        ))
        let model = makeModel()
        var opened = false

        await lifecycle.launch(model: model) { opened = true }

        XCTAssertTrue(opened)
        XCTAssertEqual(model.syncState, .waitingToSync)
        XCTAssertEqual(model.errorMessage, "MCPaste couldn’t start its local helper. Quit and reopen MCPaste.")
    }

    func testConcurrentLifecycleLaunchIsSingleFlightAndStillOpensEachRequest() async {
        let runtime = RuntimeSpy()
        let lifecycleSpy = LifecycleSpy(runtime: runtime)
        let startGate = StartGate()
        let lifecycle = AppLifecycleController(runtimeOwner: RuntimeOwner(
            start: {
                await startGate.wait()
                return try await lifecycleSpy.start()
            },
            stop: { await lifecycleSpy.stop() }
        ))
        let model = makeModel()
        let openCounter = OpenCounter()

        async let first: Void = lifecycle.launch(model: model) { openCounter.increment() }
        await waitUntil { await startGate.hasStarted() }
        async let second: Void = lifecycle.launch(model: model) { openCounter.increment() }
        await Task.yield()
        await startGate.release()
        _ = await (first, second)

        let counts = await lifecycleSpy.counts()
        XCTAssertEqual(counts.starts, 1)
        XCTAssertEqual(openCounter.value, 2)
    }

    private func makeModel(
        runtime: (any PeerRuntimeServing)? = nil,
        clock: any DebounceClock = ManualDebounceClock(),
        refreshInterval: UInt64 = 500_000_000,
        normalize: @escaping ([Data]) async throws -> [NormalizedImage] = { _ in [] }
    ) -> AppModel {
        AppModel(
            runtime: runtime,
            debounceClock: clock,
            refreshIntervalNanoseconds: refreshInterval,
            automaticallyRefresh: false,
            normalize: normalize,
            configureConnectors: { [] }
        )
    }

    private func seed(
        _ model: AppModel,
        runtime: RuntimeSpy,
        revision value: Int64,
        draft: String = "",
        attachments: [NormalizedImage] = []
    ) async {
        let assets = attachments.map {
            RuntimeAsset(
                digest: String(repeating: "b", count: 64),
                mimeType: $0.mimeType,
                width: $0.width,
                height: $0.height,
                data: $0.data
            )
        }
        await runtime.enqueueCurrent(.success(context(value, source: "local", text: draft, assets: assets)))
        await model.refreshNow()
    }

    private func image(_ byte: UInt8) -> NormalizedImage {
        NormalizedImage(mimeType: "image/png", width: Int(byte) + 1, height: Int(byte) + 2, data: Data([byte]))
    }

    private func asset(
        _ byte: UInt8,
        mime: String = "image/png",
        width: Int = 10,
        height: Int = 20
    ) -> RuntimeAsset {
        RuntimeAsset(digest: String(repeating: "a", count: 64), mimeType: mime, width: width, height: height, data: Data([byte, byte + 10]))
    }

    private func runtimeAsset(_ image: NormalizedImage, index: Int) -> RuntimeAsset {
        RuntimeAsset(
            digest: String(repeating: index == 0 ? "a" : "b", count: 64),
            mimeType: image.mimeType,
            width: image.width,
            height: image.height,
            data: image.data
        )
    }

    private func revision(_ value: Int64) -> RuntimeRevision {
        RuntimeRevision(wallMillis: value, logical: 0, deviceID: "device-\(value)")
    }

    private func context(
        _ value: Int64,
        source: String,
        date: Date? = nil,
        text: String,
        assets: [RuntimeAsset] = [],
        reachable: Bool = true,
        state: PeerSyncState = .upToDate
    ) -> RuntimeContext {
        RuntimeContext(
            revision: revision(value),
            sourceDeviceID: source,
            updatedAt: date ?? Date(timeIntervalSince1970: TimeInterval(value)),
            text: text,
            assets: assets,
            sourceReachable: reachable,
            syncState: state
        )
    }

    private func fixtureDevices(now: Date = Date(timeIntervalSince1970: 1_700_000_000)) -> [PeerDevice] {
        [
            PeerDevice(id: "local", displayName: "Blanc’s MacBook Pro", reachable: true, isLocal: true, isSource: false, lastSeenAt: now),
            PeerDevice(id: "remote", displayName: "Studio Mac", reachable: true, isLocal: false, isSource: true, lastSeenAt: now)
        ]
    }

    private func waitUntil(
        timeoutNanoseconds: UInt64 = 1_000_000_000,
        _ condition: @escaping () async -> Bool
    ) async {
        let deadline = DispatchTime.now().uptimeNanoseconds + timeoutNanoseconds
        while !(await condition()) && DispatchTime.now().uptimeNanoseconds < deadline {
            await Task.yield()
        }
        let fulfilled = await condition()
        XCTAssertTrue(fulfilled)
    }
}

private actor Counter {
    private var count = 0
    func increment() { count += 1 }
    func value() -> Int { count }
}

@MainActor
private final class OpenCounter {
    private(set) var value = 0
    func increment() { value += 1 }
}
