import Foundation
import MCPasteCore

protocol DebounceClock: Sendable {
    func sleep(nanoseconds: UInt64) async throws
}

private struct SystemDebounceClock: DebounceClock {
    func sleep(nanoseconds: UInt64) async throws {
        try await Task.sleep(nanoseconds: nanoseconds)
    }
}

@MainActor
public final class AppModel: ObservableObject {
    nonisolated static let defaultRefreshIntervalNanoseconds: UInt64 = 500_000_000

    @Published public var draft = "" {
        didSet {
            guard draft != oldValue, !isApplyingContext else { return }
            draftDidChange()
        }
    }
    @Published public private(set) var attachments: [NormalizedImage] = []
    @Published public private(set) var devices: [PeerDevice] = []
    @Published public private(set) var syncState: PeerSyncState = .waitingToSync
    @Published public private(set) var lastUpdatedBy: String?
    @Published public private(set) var lastUpdatedAt: Date?
    @Published public private(set) var errorMessage: String?
    @Published public private(set) var uploadingCount = 0
    @Published public private(set) var connectorNames: [String] = []

    private struct PublicationSnapshot {
        let text: String
        let images: [NormalizedImage]
        let baseRevision: RuntimeRevision?
        let remoteGeneration: UInt64
    }

    private enum DraftPublicationState {
        case settled
        case pending
        case failed
    }

    private let debounceClock: any DebounceClock
    private let refreshIntervalNanoseconds: UInt64
    private let automaticallyRefresh: Bool
    private let normalizeImages: ([Data]) async throws -> [NormalizedImage]
    private let configureConnectors: () async -> [String]
    private let publicationGate = SerialGate()

    private var runtime: (any PeerRuntimeServing)?
    private var runtimeWasInstalled: Bool
    private var currentRevision: RuntimeRevision?
    private var attachmentIDs: [UUID] = []
    private var refreshRequest: UInt64 = 0
    private var appliedDeviceRequest: UInt64 = 0
    private var remoteGeneration: UInt64 = 0
    private var publicationGeneration: UInt64 = 0
    private var attachmentJobs = 0
    private var draftPublicationState: DraftPublicationState = .settled
    private var isApplyingContext = false
    private var isShutDown = false
    private var debounceTask: Task<Void, Never>?
    private var draftPublicationTask: Task<Void, Never>?
    private var refreshTask: Task<Void, Never>?
    private var connectorTask: Task<Void, Never>?

    init(
        runtime: (any PeerRuntimeServing)? = nil,
        debounceClock: any DebounceClock = SystemDebounceClock(),
        refreshIntervalNanoseconds: UInt64 = AppModel.defaultRefreshIntervalNanoseconds,
        automaticallyRefresh: Bool = true,
        normalize: @escaping ([Data]) async throws -> [NormalizedImage] = AppModel.normalize,
        configureConnectors: @escaping () async -> [String] = AppModel.configureLocalConnectors
    ) {
        self.runtime = runtime
        runtimeWasInstalled = runtime != nil
        self.debounceClock = debounceClock
        self.refreshIntervalNanoseconds = refreshIntervalNanoseconds
        self.automaticallyRefresh = automaticallyRefresh
        normalizeImages = normalize
        self.configureConnectors = configureConnectors

        if runtime != nil {
            startConnectorConfiguration()
            if automaticallyRefresh { activate() }
        }
    }

    deinit {
        debounceTask?.cancel()
        draftPublicationTask?.cancel()
        refreshTask?.cancel()
        connectorTask?.cancel()
    }

    @discardableResult
    func installRuntime(_ runtime: any PeerRuntimeServing) -> Bool {
        guard !runtimeWasInstalled, self.runtime == nil, !isShutDown else { return false }
        runtimeWasInstalled = true
        self.runtime = runtime
        errorMessage = nil
        startConnectorConfiguration()
        if draftPublicationState == .pending {
            scheduleDraftPublication()
        }
        if automaticallyRefresh { activate() }
        return true
    }

    func reportRuntimeLaunchFailure() {
        guard !isShutDown else { return }
        syncState = .waitingToSync
        errorMessage = "MCPaste couldn’t start its local helper. Quit and reopen MCPaste."
    }

    func activate() {
        guard runtime != nil, refreshTask == nil, !isShutDown else { return }
        let interval = refreshIntervalNanoseconds
        refreshTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.refreshNow()
                do {
                    try await Task.sleep(nanoseconds: interval)
                } catch {
                    return
                }
            }
        }
    }

    func deactivate() async {
        guard !isShutDown else { return }
        isShutDown = true
        remoteGeneration &+= 1
        invalidateDraftIntent()
        let publicationTask = draftPublicationTask
        publicationTask?.cancel()
        refreshTask?.cancel()
        refreshTask = nil
        connectorTask?.cancel()
        connectorTask = nil
        await publicationGate.close()
        await publicationTask?.value
        draftPublicationTask = nil
        runtime = nil
    }

    func uploadImages(_ sourceData: [Data]) async {
        guard !sourceData.isEmpty, !isShutDown else { return }
        let submittedRemoteGeneration = remoteGeneration
        uploadingCount += sourceData.count
        attachmentJobs += 1
        invalidateDraftIntent()

        await publicationGate.run { [weak self] in
            await self?.normalizeAndPublish(
                sourceData,
                submittedRemoteGeneration: submittedRemoteGeneration
            )
        }

        guard !isShutDown else { return }
        uploadingCount = max(0, uploadingCount - sourceData.count)
        attachmentJobs = max(0, attachmentJobs - 1)
        resumeDraftPublicationIfNeeded()
    }

    func removeAttachment(at index: Int) async {
        guard attachments.indices.contains(index), !isShutDown else { return }
        let attachmentID = attachmentIDs[index]
        let submittedRemoteGeneration = remoteGeneration
        attachmentJobs += 1
        invalidateDraftIntent()

        await publicationGate.run { [weak self] in
            guard let self else { return }
            guard submittedRemoteGeneration == self.remoteGeneration else { return }
            guard let removalIndex = self.attachmentIDs.firstIndex(of: attachmentID) else { return }
            var replacement = self.attachments
            var replacementIDs = self.attachmentIDs
            replacement.remove(at: removalIndex)
            replacementIDs.remove(at: removalIndex)
            await self.publishAttachmentReplacement(
                replacement,
                replacementIDs: replacementIDs,
                submittedRevision: self.currentRevision,
                submittedRemoteGeneration: submittedRemoteGeneration
            )
        }

        guard !isShutDown else { return }
        attachmentJobs = max(0, attachmentJobs - 1)
        resumeDraftPublicationIfNeeded()
    }

    func clearContext() async {
        guard !isShutDown else { return }
        let submittedRemoteGeneration = remoteGeneration
        attachmentJobs += 1
        invalidateDraftIntent()
        let submittedPublicationGeneration = publicationGeneration

        await publicationGate.run { [weak self] in
            guard let self, submittedRemoteGeneration == self.remoteGeneration else { return }
            await self.publishClear(
                submittedRevision: self.currentRevision,
                submittedRemoteGeneration: submittedRemoteGeneration,
                submittedPublicationGeneration: submittedPublicationGeneration
            )
        }

        guard !isShutDown else { return }
        attachmentJobs = max(0, attachmentJobs - 1)
        resumeDraftPublicationIfNeeded()
    }

    func refreshNow() async {
        guard let runtime, !isShutDown else { return }
        refreshRequest &+= 1
        let request = refreshRequest

        async let currentResult = Self.fetchCurrent(from: runtime)
        async let deviceResult = Self.fetchDevices(from: runtime)
        let (fetchedCurrent, fetchedDevices) = await (currentResult, deviceResult)
        guard !isShutDown else { return }

        if request > appliedDeviceRequest, case .success(let fetched) = fetchedDevices {
            appliedDeviceRequest = request
            devices = fetched
        }

        switch fetchedCurrent {
        case .success(let context?):
            apply(context)
        case .success(nil):
            break
        case .failure(.empty):
            if currentRevision == nil, draftPublicationState == .settled { syncState = .upToDate }
        case .failure(.sourceOffline):
            syncState = .sourceOffline
            errorMessage = nil
        case .failure(.rejectedImage):
            errorMessage = "Shared images couldn’t be displayed."
        case .failure(.unavailable), .failure(.invalidResponse), .failure(.conflict):
            syncState = .waitingToSync
            errorMessage = "MCPaste couldn’t refresh its shared context. Check that MCPaste is still running."
        }
    }

    private func draftDidChange() {
        guard !isShutDown else { return }
        draftPublicationState = .pending
        syncState = runtime == nil ? .waitingToSync : .updating
        errorMessage = nil
        invalidateDraftIntent()
        guard runtime != nil, attachmentJobs == 0 else { return }
        scheduleDraftPublication()
    }

    private func scheduleDraftPublication() {
        guard
            runtime != nil,
            !isShutDown,
            attachmentJobs == 0,
            debounceTask == nil,
            draftPublicationTask == nil
        else { return }
        let generation = publicationGeneration
        let snapshot = PublicationSnapshot(
            text: draft,
            images: attachments,
            baseRevision: currentRevision,
            remoteGeneration: remoteGeneration
        )
        let clock = debounceClock
        debounceTask = Task { [weak self] in
            do {
                try await clock.sleep(nanoseconds: 1_000_000_000)
            } catch {
                return
            }
            guard !Task.isCancelled else { return }
            self?.startDraftPublication(snapshot, generation: generation)
        }
    }

    private func startDraftPublication(_ snapshot: PublicationSnapshot, generation: UInt64) {
        guard
            !isShutDown,
            generation == publicationGeneration,
            snapshot.remoteGeneration == remoteGeneration
        else { return }
        debounceTask = nil
        let gate = publicationGate
        draftPublicationTask = Task { [weak self] in
            await gate.run { [weak self] in
                await self?.publishDraft(snapshot, generation: generation)
            }
            self?.draftPublicationFinished()
        }
    }

    private func publishDraft(_ snapshot: PublicationSnapshot, generation: UInt64) async {
        guard
            let runtime,
            !isShutDown,
            generation == publicationGeneration,
            snapshot.remoteGeneration == remoteGeneration,
            revisionsEqual(snapshot.baseRevision, currentRevision)
        else { return }

        do {
            let result = try await runtime.publish(
                text: snapshot.text,
                images: snapshot.images,
                expectedRevision: snapshot.baseRevision
            )
            guard
                !isShutDown,
                snapshot.remoteGeneration == remoteGeneration,
                revisionsEqual(snapshot.baseRevision, currentRevision)
            else { return }
            currentRevision = result.revision
            if
                generation == publicationGeneration,
                draft == snapshot.text,
                attachments == snapshot.images
            {
                draftPublicationState = .settled
                syncState = result.syncState
                errorMessage = nil
            }
        } catch is CancellationError {
            return
        } catch {
            guard
                generation == publicationGeneration,
                snapshot.remoteGeneration == remoteGeneration,
                !isShutDown
            else { return }
            draftPublicationState = .failed
            syncState = .waitingToSync
            errorMessage = Self.publicationFailureMessage
        }
    }

    private func draftPublicationFinished() {
        draftPublicationTask = nil
        resumeDraftPublicationIfNeeded()
    }

    private func normalizeAndPublish(
        _ sourceData: [Data],
        submittedRemoteGeneration: UInt64
    ) async {
        do {
            let normalized = try await normalizeImages(sourceData)
            guard
                !isShutDown,
                submittedRemoteGeneration == remoteGeneration
            else { return }
            let merged = attachments + normalized
            try ImageNormalizer.validateAttachmentCount(merged.count)
            guard merged.reduce(0, { $0 + $1.data.count }) <= ImageNormalizer.maxBundleBytes else {
                throw ImageNormalizationError.tooLarge
            }
            await publishAttachmentReplacement(
                merged,
                replacementIDs: attachmentIDs + normalized.map { _ in UUID() },
                submittedRevision: currentRevision,
                submittedRemoteGeneration: submittedRemoteGeneration
            )
        } catch {
            guard !isShutDown else { return }
            errorMessage = Self.attachmentFailureMessage(for: error)
        }
    }

    private func publishAttachmentReplacement(
        _ replacement: [NormalizedImage],
        replacementIDs: [UUID],
        submittedRevision: RuntimeRevision?,
        submittedRemoteGeneration: UInt64
    ) async {
        guard
            let runtime,
            !isShutDown,
            replacement.count == replacementIDs.count,
            submittedRemoteGeneration == remoteGeneration,
            revisionsEqual(submittedRevision, currentRevision)
        else { return }

        var expectedRevision = submittedRevision
        while !Task.isCancelled {
            let text = draft
            syncState = .updating
            errorMessage = nil
            do {
                let result = try await runtime.publish(
                    text: text,
                    images: replacement,
                    expectedRevision: expectedRevision
                )
                guard
                    !isShutDown,
                    submittedRemoteGeneration == remoteGeneration,
                    revisionsEqual(expectedRevision, currentRevision)
                else { return }
                currentRevision = result.revision
                expectedRevision = result.revision
                syncState = result.syncState
            } catch is CancellationError {
                return
            } catch {
                guard !isShutDown, submittedRemoteGeneration == remoteGeneration else { return }
                draftPublicationState = .failed
                syncState = .waitingToSync
                errorMessage = Self.publicationFailureMessage
                return
            }
            guard
                !isShutDown,
                submittedRemoteGeneration == remoteGeneration,
                revisionsEqual(expectedRevision, currentRevision)
            else { return }
            guard draft == text else { continue }
            attachments = replacement
            attachmentIDs = replacementIDs
            draftPublicationState = .settled
            errorMessage = nil
            return
        }
    }

    private func publishClear(
        submittedRevision: RuntimeRevision?,
        submittedRemoteGeneration: UInt64,
        submittedPublicationGeneration: UInt64
    ) async {
        guard
            let runtime,
            !isShutDown,
            submittedRemoteGeneration == remoteGeneration,
            submittedPublicationGeneration == publicationGeneration,
            revisionsEqual(submittedRevision, currentRevision)
        else { return }
        syncState = .updating
        errorMessage = nil
        do {
            let result = try await runtime.publish(
                text: "",
                images: [],
                expectedRevision: submittedRevision
            )
            guard
                !isShutDown,
                submittedRemoteGeneration == remoteGeneration,
                revisionsEqual(submittedRevision, currentRevision)
            else { return }
            currentRevision = result.revision
            guard submittedPublicationGeneration == publicationGeneration else { return }
            syncState = result.syncState
        } catch is CancellationError {
            return
        } catch {
            guard
                !isShutDown,
                submittedRemoteGeneration == remoteGeneration,
                submittedPublicationGeneration == publicationGeneration
            else { return }
            draftPublicationState = .failed
            syncState = .waitingToSync
            errorMessage = Self.publicationFailureMessage
            return
        }
        isApplyingContext = true
        draft = ""
        attachments = []
        attachmentIDs = []
        isApplyingContext = false
        draftPublicationState = .settled
        errorMessage = nil
    }

    private func apply(_ context: RuntimeContext) {
        if let currentRevision {
            guard context.revision >= currentRevision else { return }
            if context.revision == currentRevision {
                if draftPublicationState == .settled, attachmentJobs == 0 {
                    syncState = context.sourceReachable ? context.syncState : .sourceOffline
                }
                return
            }
        }

        remoteGeneration &+= 1
        invalidateDraftIntent()
        let images = context.assets.map {
            NormalizedImage(mimeType: $0.mimeType, width: $0.width, height: $0.height, data: $0.data)
        }
        isApplyingContext = true
        draft = context.text
        attachments = images
        attachmentIDs = images.map { _ in UUID() }
        isApplyingContext = false
        currentRevision = context.revision
        draftPublicationState = .settled
        lastUpdatedBy = devices.first { $0.id == context.sourceDeviceID }?.displayName
        lastUpdatedAt = context.updatedAt
        syncState = context.sourceReachable ? context.syncState : .sourceOffline
        errorMessage = nil
    }

    private func invalidateDraftIntent() {
        publicationGeneration &+= 1
        debounceTask?.cancel()
        debounceTask = nil
    }

    private func resumeDraftPublicationIfNeeded() {
        guard attachmentJobs == 0, draftPublicationState == .pending else { return }
        scheduleDraftPublication()
    }

    private func startConnectorConfiguration() {
        guard connectorTask == nil, !isShutDown else { return }
        let configure = configureConnectors
        connectorTask = Task { [weak self] in
            let names = await configure()
            guard !Task.isCancelled else { return }
            self?.storeConnectorNames(names)
        }
    }

    private func storeConnectorNames(_ names: [String]) {
        guard !isShutDown else { return }
        connectorNames = names
        connectorTask = nil
    }

    private func revisionsEqual(_ lhs: RuntimeRevision?, _ rhs: RuntimeRevision?) -> Bool {
        switch (lhs, rhs) {
        case (nil, nil): return true
        case (.some(let lhs), .some(let rhs)): return lhs == rhs
        default: return false
        }
    }

    private static func fetchCurrent(
        from runtime: any PeerRuntimeServing
    ) async -> Result<RuntimeContext?, PeerRuntimeError> {
        do {
            return .success(try await runtime.current())
        } catch let error as PeerRuntimeError {
            return .failure(error)
        } catch {
            return .failure(.unavailable)
        }
    }

    private static func fetchDevices(
        from runtime: any PeerRuntimeServing
    ) async -> Result<[PeerDevice], PeerRuntimeError> {
        do {
            return .success(try await runtime.devices())
        } catch let error as PeerRuntimeError {
            return .failure(error)
        } catch {
            return .failure(.unavailable)
        }
    }

    private static func normalize(_ sourceData: [Data]) async throws -> [NormalizedImage] {
        try await Task.detached(priority: .userInitiated) {
            let normalizer = ImageNormalizer()
            return try sourceData.map { try normalizer.normalize($0) }
        }.value
    }

    private static func configureLocalConnectors() async -> [String] {
        guard let cliURL = ConnectorSetup.embeddedCLIURL() else { return [] }
        let setup = ConnectorSetup(cliURL: cliURL, credentialURL: ConnectorSetup.credentialFileURL())
        return (try? await setup.run()) ?? []
    }

    private static let publicationFailureMessage =
        "Changes couldn’t be published. Check that MCPaste is still running."

    private static func attachmentFailureMessage(for error: Error) -> String {
        switch error {
        case ImageNormalizationError.tooLarge:
            return "A shared context holds up to 8 images and 32 MB in total."
        case ImageNormalizationError.animated:
            return "Animated images aren’t supported."
        case ImageNormalizationError.unsupported, ImageNormalizationError.malformed:
            return "That image format isn’t supported."
        case ImageNormalizationError.encodingFailed:
            return "That image couldn’t be prepared."
        default:
            return "Images couldn’t be added."
        }
    }
}
