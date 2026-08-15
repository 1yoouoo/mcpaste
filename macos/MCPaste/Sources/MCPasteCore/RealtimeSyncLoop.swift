import Foundation

public final class RealtimeSyncLoop {
    public enum Phase: Equatable, Sendable {
        case realtime
        case polling
    }

    public typealias OpenEvents = @Sendable () async throws -> Void
    public typealias Refresh = @Sendable () async -> Void
    public typealias Sleep = @Sendable (UInt64) async throws -> Void
    public typealias OnPhase = @Sendable (Phase) async -> Void

    private let openEvents: OpenEvents
    private let refresh: Refresh
    private let sleep: Sleep
    private let onPhase: OnPhase

    public init(
        openEvents: @escaping OpenEvents,
        refresh: @escaping Refresh,
        sleep: @escaping Sleep = { nanoseconds in try await Task.sleep(nanoseconds: nanoseconds) },
        onPhase: @escaping OnPhase = { _ in }
    ) {
        self.openEvents = openEvents
        self.refresh = refresh
        self.sleep = sleep
        self.onPhase = onPhase
    }

    public func run(iterations: Int? = nil) async {
        guard iterations != 0 else { return }
        var completed = 0

        while !Task.isCancelled {
            await onPhase(.realtime)
            do {
                try await openEvents()
            } catch is CancellationError {
                return
            } catch {
                // Any SSE failure falls through to the same refresh-and-retry path.
            }

            guard !Task.isCancelled else { return }
            await onPhase(.polling)
            await refresh()
            guard !Task.isCancelled else { return }

            do {
                try await sleep(15_000_000_000)
            } catch {
                return
            }

            completed += 1
            if let iterations, completed >= iterations { return }
        }
    }
}
