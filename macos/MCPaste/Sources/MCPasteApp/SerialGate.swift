import Foundation

/// Runs submitted work one item at a time, in submission order.
actor SerialGate {
    private var tail: Task<Void, Never>?
    private var tasks: [UUID: Task<Void, Never>] = [:]
    private var isClosed = false

    func run(_ work: @escaping () async -> Void) async {
        guard !isClosed else { return }
        let previous = tail
        let id = UUID()
        let task = Task {
            await previous?.value
            guard !Task.isCancelled else { return }
            await work()
        }
        tasks[id] = task
        tail = task
        if Task.isCancelled { task.cancel() }
        await withTaskCancellationHandler(operation: {
            await task.value
        }, onCancel: {
            task.cancel()
        })
        tasks[id] = nil
    }

    func cancelAll() async {
        let active = Array(tasks.values)
        active.forEach { $0.cancel() }
        for task in active {
            await task.value
        }
    }

    func close() async {
        isClosed = true
        await cancelAll()
        tail = nil
    }
}
