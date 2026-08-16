import Foundation

/// Runs submitted work one item at a time, in submission order.
actor SerialGate {
    private var tail: Task<Void, Never>?

    func run(_ work: @escaping () async -> Void) async {
        let previous = tail
        let task = Task {
            await previous?.value
            await work()
        }
        tail = task
        await task.value
    }
}
