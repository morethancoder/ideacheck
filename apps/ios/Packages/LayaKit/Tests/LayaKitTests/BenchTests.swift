import CoreML
import Foundation
import Testing
@testable import LayaKit

/// Latency per length bucket and per compute unit, on demand:
///   LAYAKIT_BENCH=1 swift test -c release -Xswiftc -enable-testing --filter BenchTests
@Suite(.serialized)
struct BenchTests {
    static let on = ProcessInfo.processInfo.environment["LAYAKIT_BENCH"] != nil
    static let name = ProcessInfo.processInfo.environment["LAYAKIT_BENCH_MODEL"] ?? "laya-typed-decisions-coreml"

    static func measure(_ judge: LayaJudge, _ state: LayaState, _ q: LayaQuestion, runs: Int = 10) throws -> Double {
        _ = try judge.evaluate(state: state, question: q)
        var times: [Double] = []
        for _ in 0..<runs {
            let t = ContinuousClock.now
            _ = try judge.evaluate(state: state, question: q)
            let d = ContinuousClock.now - t
            times.append(Double(d.components.seconds) * 1000 + Double(d.components.attoseconds) / 1e15)
        }
        return times.sorted()[runs / 2]
    }

    /// What the OS charges the process (Xcode's memory gauge, jetsam's measure).
    static func footprintMB() -> Double {
        var info = task_vm_info_data_t()
        var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<natural_t>.size)
        let kr = withUnsafeMutablePointer(to: &info) {
            $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), $0, &count) }
        }
        return kr == KERN_SUCCESS ? Double(info.phys_footprint) / 1_048_576 : -1
    }

    @Test(.enabled(if: on)) func buckets() throws {
        try #require(Local.hasModel(Self.name))
        let model = try LayaModel(directory: Local.checkpoint(Self.name), keepLoaded: 1)
        let judge = LayaJudge(model: model)
        let q = LayaQuestion.noul("Does the customer ask for money back?")
        for words in [20, 80, 180, 350, 700] {
            let state = LayaState.text(String(repeating: "refund please ", count: words / 2))
            let n = try model.sequence(state: state, question: q).ids.count
            let t0 = ContinuousClock.now
            _ = try judge.evaluate(state: state, question: q)
            let first = ContinuousClock.now - t0
            let p50 = try Self.measure(judge, state, q)
            print(String(format: "bench %@ %d tokens (bucket %d): first call %@, p50 %.1f ms, footprint %.0f MB", Self.name, n, model.buckets.first { $0 >= n } ?? 0, "\(first)", p50, Self.footprintMB()))
        }
    }

    @Test(.enabled(if: on), arguments: [MLComputeUnits.cpuOnly, .cpuAndGPU, .all, .cpuAndNeuralEngine])
    func units(_ units: MLComputeUnits) throws {
        try #require(Local.hasModel(Self.name))
        let t0 = ContinuousClock.now
        let model = try LayaModel(directory: Local.checkpoint(Self.name), computeUnits: units)
        let load = ContinuousClock.now - t0
        let p50 = try Self.measure(LayaJudge(model: model), .text("I was charged twice for invoice 4411, please refund it today."), .noul("Does the customer ask for money back?"))
        print(String(format: "bench %@ %@: load %@, p50 %.1f ms", Self.name, CheckpointTests.units(units), "\(load)", p50))
    }
}
