import CoreML
import Foundation
import Testing
@testable import LayaKit

/// Real checkpoints through Core ML, against what laya-coreml's own Agent
/// answered for the same inputs on this machine (Fixtures/answers-<name>.json,
/// `scripts/fixtures.py --answers`): laya's validation cases (the ones its
/// validation.json scored) and ideacheck questions. Each checkpoint is
/// skipped unless scripts/fetch-model.sh has put it in .models.
@Suite(.serialized)
struct CheckpointTests {
    static let checkpoints = ["laya-typed-decisions-coreml", "laya-multilingual-coreml-ane"]
    /// laya-coreml's own release gate for probability drift.
    static let gate = 0.02

    @Test(arguments: checkpoints)
    func reproducesPythonAnswers(checkpoint: String) throws {
        try #require(Local.hasModel(checkpoint), "no checkpoint: scripts/fetch-model.sh aac6fef/\(checkpoint)")
        let model = try Local.model(checkpoint)
        let judge = LayaJudge(model: model)
        var worst = 0.0, worstIdeacheck = 0.0, agreed = 0, total = 0
        for c in try Fixture.json("answers-\(checkpoint).json").arrayValue ?? [] {
            let name = c["name"]?.stringValue ?? "?"
            let ideacheck = c["group"]?.stringValue == "ideacheck"
            let got = try judge.evaluate(state: .json(c["state"] ?? .null), questions: c["questions"] ?? .object([]))
            for ((qid, answer), want) in zip(got, c["answers"]?.arrayValue ?? []) {
                try #require(qid == want["id"]?.stringValue)
                let p = (want["probabilities"]?.arrayValue ?? []).compactMap(\.doubleValue)
                try #require(p.count == answer.probabilities.count, "\(name)/\(qid)")
                let diff = zip(p, answer.probabilities.map(\.p)).map { abs($0 - $1) }.max() ?? 0
                worst = max(worst, diff)
                if ideacheck { worstIdeacheck = max(worstIdeacheck, diff) }
                total += 1
                if answer.selected == want["selected"]?.stringValue { agreed += 1 }
                #expect(answer.selected == want["selected"]?.stringValue, "\(name)/\(qid): \(answer.probabilities) vs \(p)")
                #expect(diff < Self.gate, "\(name)/\(qid) drifted \(diff)")
            }
        }
        print("laya parity \(checkpoint) (\(Self.units(model.computeUnits))): \(agreed)/\(total) selected answers agree; max |Δp| \(worst) (ideacheck questions \(worstIdeacheck))")
        #expect(total >= 20)
    }

    /// Per-decision latency on this machine, question in to answer out:
    /// tokenizing, arrays, one prediction and decoding. Loading, compiling
    /// and the first call at each length are excluded.
    @Test(arguments: checkpoints)
    func latencyPerDecision(checkpoint: String) throws {
        try #require(Local.hasModel(checkpoint), "no checkpoint: scripts/fetch-model.sh aac6fef/\(checkpoint)")
        let model = try Local.model(checkpoint)
        let judge = LayaJudge(model: model)
        let q = LayaQuestion.noul("Does the customer ask for money back?")
        let sentence = "The customer reports duplicate billing and requests a refund today. "
        var cases: [(String, LayaState)] = [("short", .text("I was charged twice for invoice 4411, please refund it today."))]
        if model.maxLength > 128 {
            let idea = try JSONValue(parsing: #"{"idea": {"audience": "Independent dental practices in the US with 1-5 chairs", "problem": "Front desks lose about a fifth of bookings to no-shows and spend hours a week calling patients to confirm.", "text": "An SMS assistant that confirms appointments, fills cancellations from a waitlist, and syncs with Dentrix."}}"#)
            cases += [
                ("ideacheck idea", .json(idea)),
                ("~500 tokens", .text(String(repeating: sentence, count: 35))),
                ("1024 tokens", .text(String(repeating: sentence, count: 200))),
            ]
        }
        for (label, state) in cases {
            let tokens = try model.sequence(state: state, question: q).ids.count
            _ = try judge.evaluate(state: state, question: q)
            var times: [Double] = []
            for _ in 0..<20 {
                let t = ContinuousClock.now
                _ = try judge.evaluate(state: state, question: q)
                let d = ContinuousClock.now - t
                times.append(Double(d.components.seconds) * 1000 + Double(d.components.attoseconds) / 1e15)
            }
            times.sort()
            print(String(format: "laya latency %@ (%@) %@: %d tokens, p50 %.1f ms, p90 %.1f ms", checkpoint,
                         Self.units(model.computeUnits), label, tokens, times[10], times[18]))
        }
    }

    static func units(_ u: MLComputeUnits) -> String {
        switch u {
        case .cpuOnly: "CPU"
        case .cpuAndGPU: "CPU+GPU"
        case .cpuAndNeuralEngine: "CPU+ANE"
        default: "all"
        }
    }
}
