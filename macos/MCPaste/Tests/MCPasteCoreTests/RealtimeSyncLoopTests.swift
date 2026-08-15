import XCTest
@testable import MCPasteCore

final class RealtimeSyncLoopTests: XCTestCase {
    func testSSEFailureRefreshesImmediatelyThenPollsBeforeRetry() async {
        let recorder = LoopRecorder()
        let loop = RealtimeSyncLoop(
            openEvents: { throw APIError.transport },
            refresh: { await recorder.record(.refresh) },
            sleep: { _ in await recorder.record(.sleep) }
        )

        await loop.run(iterations: 2)

        let events = await recorder.snapshot()
        XCTAssertEqual(events, [.refresh, .sleep, .refresh, .sleep])
    }

    func testPhaseReportsRealtimeAttemptThenPollingFallback() async {
        let recorder = PhaseRecorder()
        let loop = RealtimeSyncLoop(
            openEvents: { throw APIError.transport },
            refresh: {},
            sleep: { _ in },
            onPhase: { await recorder.record($0) }
        )

        await loop.run(iterations: 2)

        let phases = await recorder.snapshot()
        XCTAssertEqual(phases, [.realtime, .polling, .realtime, .polling])
    }

    func testCancellationStopsWithoutAnotherRefresh() async {
        let recorder = LoopRecorder()
        let loop = RealtimeSyncLoop(
            openEvents: {},
            refresh: { await recorder.record(.refresh) },
            sleep: { _ in
                await recorder.record(.sleep)
                throw CancellationError()
            }
        )

        await loop.run()

        let events = await recorder.snapshot()
        XCTAssertEqual(events, [.refresh, .sleep])
    }
}

private actor PhaseRecorder {
    private(set) var phases: [RealtimeSyncLoop.Phase] = []

    func record(_ phase: RealtimeSyncLoop.Phase) { phases.append(phase) }
    func snapshot() -> [RealtimeSyncLoop.Phase] { phases }
}

private actor LoopRecorder {
    enum Event: Equatable {
        case refresh
        case sleep
    }

    private(set) var events: [Event] = []

    func record(_ event: Event) { events.append(event) }
    func snapshot() -> [Event] { events }
}
