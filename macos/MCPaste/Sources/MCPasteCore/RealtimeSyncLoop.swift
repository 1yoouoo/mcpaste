import Foundation

public final class RealtimeSyncLoop {
    public typealias OpenEvents = @Sendable () async throws -> Void
    public typealias Refresh = @Sendable () async -> Void
    public typealias Sleep = @Sendable (UInt64) async throws -> Void

    private let openEvents: OpenEvents
    private let refresh: Refresh
    private let sleep: Sleep

    public init(
        openEvents: @escaping OpenEvents,
        refresh: @escaping Refresh,
        sleep: @escaping Sleep = { nanoseconds in try await Task.sleep(nanoseconds: nanoseconds) }
    ) {
        self.openEvents = openEvents
        self.refresh = refresh
        self.sleep = sleep
    }

    public func run(iterations: Int? = nil) async {
        guard iterations != 0 else { return }
        var completed = 0

        while !Task.isCancelled {
            do {
                try await openEvents()
            } catch is CancellationError {
                return
            } catch {
                // Any SSE failure falls through to the same refresh-and-retry path.
            }

            guard !Task.isCancelled else { return }
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
