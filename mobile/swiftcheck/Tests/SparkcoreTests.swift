import Foundation
import FoundationModels
import Sparkcore
import XCTest

/// A deterministic judge: the first option, the top level, and "probably
/// true", for every question — enough to drive a whole check.
final class StubJudge: NSObject, SparkcoreJudgeProtocol, @unchecked Sendable {
    private let lock = NSLock()
    private(set) var asked = 0

    func name() -> String { "stub" }

    func evaluate(_ requestJSON: String?, error: NSErrorPointer) -> String {
        sparkcoreCatching(error) { try answer(requestJSON ?? "") }
    }

    private func answer(_ requestJSON: String) throws -> String {
        lock.withLock { asked += 1 }
        let request = try JSONDecoder().decode(SparkcoreRequest.self, from: Data(requestJSON.utf8))
        switch request.question.kind {
        case "choice": return #"{"choice": "\#(request.question.options![0].key)"}"#
        case "score": return #"{"level": \#(request.question.levels!.count - 1)}"#
        default: return #"{"probability": 0.8}"#
        }
    }
}

final class Events: NSObject, SparkcoreListenerProtocol, @unchecked Sendable {
    private let lock = NSLock()
    private let start = Date()
    private(set) var all: [[String: Any]] = []
    /// When each event arrived, in seconds since the listener was made.
    private(set) var at: [TimeInterval] = []

    func onEvent(_ eventJSON: String?) {
        guard let data = eventJSON?.data(using: .utf8),
            let event = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else { return }
        lock.withLock {
            all.append(event)
            at.append(Date().timeIntervalSince(start))
        }
    }

    /// Seconds from each stage's first event to its last, in order: where a
    /// check spends its time.
    var stages: String {
        lock.withLock {
            var order: [String] = []
            var span: [String: (TimeInterval, TimeInterval, Int)] = [:]
            for (event, t) in zip(all, at) {
                let stage = event["stage"] as? String ?? "?"
                if span[stage] == nil { order.append(stage) }
                let s = span[stage] ?? (t, t, 0)
                span[stage] = (s.0, t, s.2 + ((event["type"] as? String) == "started" ? 1 : 0))
            }
            return order.map { "\($0) \(String(format: "%.1f", span[$0]!.1 - span[$0]!.0))s (\(span[$0]!.2) asked)" }.joined(separator: ", ")
        }
    }
}

let idea = #"{"idea": "A scheduling app for dental clinics that fills cancelled slots from a waitlist by text message.", "fields": {"audience": "independent dental clinics"}}"#

func object(_ json: String) throws -> [String: Any] {
    try XCTUnwrap(JSONSerialization.jsonObject(with: Data(json.utf8)) as? [String: Any])
}

final class SparkcoreTests: XCTestCase {
    func testACheckRunsInSwiftAndReturnsAVerdict() throws {
        let judge = StubJudge()
        let events = Events()
        let engine = try SparkcoreEngine.make(settingsJSON: #"{"max_concurrent": 4}"#, judge: judge)

        let result = try object(try engine.check(idea, listener: events))

        XCTAssertEqual(result["status"] as? String, "ok")
        XCTAssertEqual(result["backend"] as? String, "stub")
        let verdict = try XCTUnwrap(result["verdict"] as? String)
        XCTAssertTrue(["build", "explore", "park", "kill"].contains(verdict), verdict)
        XCTAssertGreaterThan(judge.asked, 5)
        XCTAssertFalse(events.all.isEmpty)
    }

    func testCancelEndsTheCheckWithAnError() throws {
        final class Slow: NSObject, SparkcoreJudgeProtocol, @unchecked Sendable {
            func name() -> String { "slow" }
            func evaluate(_ requestJSON: String?, error: NSErrorPointer) -> String {
                Thread.sleep(forTimeInterval: 5)
                return #"{"probability": 0.5}"#
            }
        }
        let engine = try SparkcoreEngine.make(judge: Slow())
        let check = try engine.prepare(idea, optionsJSON: "")
        DispatchQueue.global().asyncAfter(deadline: .now() + 0.2) { check.cancel() }
        let start = Date()
        XCTAssertThrowsError(try check.run(nil))
        XCTAssertLessThan(Date().timeIntervalSince(start), 2)
    }

    func testCataloguesForTheForms() throws {
        let fields = try object(try sparkcoreFields())
        XCTAssertNotNil(fields["idea"])
        let rubrics = try object(try sparkcoreRubrics())
        XCTAssertNotNil(rubrics["business"])
    }

    /// Engine overhead: a whole check with a judge that answers at once.
    func testEngineOverhead() throws {
        let engine = try SparkcoreEngine.make(settingsJSON: #"{"max_concurrent": 4}"#, judge: StubJudge())
        _ = try engine.check(idea, listener: nil)  // warm: first load parses the embedded configs
        let runs = 10
        let start = Date()
        for _ in 0..<runs { _ = try engine.check(idea, listener: nil) }
        let ms = Date().timeIntervalSince(start) * 1000 / Double(runs)
        print("sparkcore: engine overhead \(String(format: "%.1f", ms)) ms per check (stub judge)")
    }

    /// One real choice question through the on-device model, when this
    /// simulator's host can run it.
    func testFoundationJudgeAnswersARealChoice() throws {
        guard #available(iOS 26.0, *) else { throw XCTSkip("needs iOS 26") }
        if let problem = SystemLanguageModel.default.availability.problem {
            throw XCTSkip("Foundation Models unavailable: \(problem)")
        }
        let request: [String: Any] = [
            "question": [
                "id": "idea_type", "kind": "choice",
                "instructions": "What kind of idea is this?",
                "options": [
                    ["key": "business", "description": "a product or service meant to make money"],
                    ["key": "creative", "description": "a work of art, fiction or music"],
                    ["key": "other", "description": "none of these"],
                ],
            ],
            "state": ["idea": "A subscription app that helps dental clinics fill cancelled slots."],
            "system": "You answer typed questions about the given STATE only.",
            "prompt": """
                STATE: {"idea": "A subscription app that helps dental clinics fill cancelled slots."}

                QUESTION id=idea_type kind=choice
                What kind of idea is this?
                Answer shape: {"choice": "<option key>"}
                """,
        ]
        let json = String(decoding: try JSONSerialization.data(withJSONObject: request), as: UTF8.self)
        let start = Date()
        var failure: NSError?
        let reply = FoundationJudge().evaluate(json, error: &failure)
        if let failure { throw failure }
        let answer = try object(reply)
        print("sparkcore: foundation answered \(answer) in \(Int(Date().timeIntervalSince(start) * 1000)) ms")
        XCTAssertTrue(["business", "creative", "other"].contains(answer["choice"] as? String ?? ""))
    }

    /// The whole check on the on-device model, judge and writer both, one
    /// question per call.
    func testAFullCheckOnTheOnDeviceModel() throws {
        try fullOnDeviceCheck(batch: false)
    }

    /// The same check with the questions sharing a state answered in one
    /// response each (FoundationJudge as a BatchJudge).
    func testAFullCheckOnTheOnDeviceModelInBatches() throws {
        try fullOnDeviceCheck(batch: true)
    }

    func fullOnDeviceCheck(batch: Bool) throws {
        guard #available(iOS 26.0, *) else { throw XCTSkip("needs iOS 26") }
        if let problem = SystemLanguageModel.default.availability.problem {
            throw XCTSkip("Foundation Models unavailable: \(problem)")
        }
        let settings = #"{"max_concurrent": 1, "question_seconds": 60, "writer_seconds": 600}"#
        let engine = batch
            ? try SparkcoreEngine.make(settingsJSON: settings, batchJudge: FoundationJudge(), writer: FoundationWriter())
            : try SparkcoreEngine.make(settingsJSON: settings, judge: FoundationJudge(), writer: FoundationWriter())
        let label = batch ? "batched" : "one by one"
        let start = Date()
        let events = Events()
        let result = try object(try engine.check(idea, listener: events))
        let answers = result["answers"] as? [[String: Any]] ?? []
        let failed = answers.filter { ($0["error"] as? String).map { !$0.hasPrefix("no ") } ?? false }
        print("sparkcore: on-device check (\(label)) \(result["verdict"] ?? "-") \(result["composite"] ?? "-") in \(Int(Date().timeIntervalSince(start)))s, \(answers.count) answers, \(failed.count) failed: \(failed.map { "\($0["id"]!): \($0["error"]!)" })")
        print("sparkcore: stages \(events.stages); warnings \(result["warnings"] ?? "-")")
        let judged = answers.compactMap { $0["latency_ms"] as? Int }.filter { $0 > 0 }
        print("sparkcore: \(Set(judged).count) distinct model calls, mean \(judged.reduce(0, +) / max(judged.count, 1)) ms, slowest \(judged.max() ?? 0) ms")
        let picks = answers.map { a in
            "\(a["id"]!)=\(a["choice"] ?? a["score"] ?? a["noul"] ?? "-")"
        }
        print("sparkcore: answers \(picks.joined(separator: " "))")
        print("sparkcore: extracted \(result["extracted"] ?? "-"); summary \(result["summary"] ?? "-")")
        XCTAssertEqual(result["status"] as? String, "ok")
        XCTAssertNotNil(result["verdict"])
    }
}
