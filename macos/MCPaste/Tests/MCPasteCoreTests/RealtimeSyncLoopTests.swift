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

private actor LoopRecorder {
    enum Event: Equatable {
        case refresh
        case sleep
    }

    private(set) var events: [Event] = []

    func record(_ event: Event) { events.append(event) }
    func snapshot() -> [Event] { events }
}
