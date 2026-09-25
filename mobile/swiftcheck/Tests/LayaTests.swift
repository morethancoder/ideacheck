import Foundation
import LayaKit
import Sparkcore
import XCTest

/// Laya through the Sparkcore judge protocol, in an iOS process. Needs a
/// downloaded checkpoint, named by LAYAKIT_CHECKPOINT (the simulator reads
/// the host's disk):
///
///   apps/ios/Packages/LayaKit/scripts/fetch-model.sh
///   LAYAKIT_CHECKPOINT=$PWD/apps/ios/Packages/LayaKit/.models/laya-typed-decisions-coreml make mobile ARGS=test
///
/// Skipped without one. The simulator runs Core ML on the CPU, so the times
/// printed here are not a phone's.
final class LayaTests: XCTestCase {
    var checkpoint: URL {
        get throws {
            guard let path = ProcessInfo.processInfo.environment["LAYAKIT_CHECKPOINT"], !path.isEmpty else {
                throw XCTSkip("set LAYAKIT_CHECKPOINT to a downloaded Laya Core ML checkpoint")
            }
            return URL(fileURLWithPath: path)
        }
    }

    /// One request in the core's shape, one answer the core accepts.
    func testLayaAnswersARequestWithProbabilities() throws {
        guard #available(iOS 26.0, *) else { throw XCTSkip("needs iOS 26") }
        let judge = LayaDeviceJudge(directory: try checkpoint)
        XCTAssertEqual(judge.name(), "laya")
        let request: [String: Any] = [
            "question": [
                "id": "idea_type", "kind": "choice", "instructions": "What kind of idea is this?",
                "options": [
                    ["key": "business", "description": "A product, service, or startup intended to make money"],
                    ["key": "creative", "description": "A game, story, artwork, music, or other creative work"],
                    ["key": "other", "description": "None of the above"],
                ],
            ],
            "state": ["idea": ["text": "A subscription app that helps dental clinics fill cancelled slots."]],
            "system": "unused by laya", "prompt": "unused by laya",
        ]
        let json = String(decoding: try JSONSerialization.data(withJSONObject: request), as: UTF8.self)
        var failure: NSError?
        let reply = judge.evaluate(json, error: &failure)
        if let failure { throw failure }
        let answer = try object(reply)
        XCTAssertEqual(answer["choice"] as? String, "business", reply)
        let probabilities = try XCTUnwrap(answer["probabilities"] as? [String: Double])
        XCTAssertEqual(probabilities.values.reduce(0, +), 1, accuracy: 1e-3)
        XCTAssertEqual(answer["method"] as? String, "laya")
    }

    /// A whole check with Laya as the judge.
    func testAFullCheckOnLaya() throws {
        guard #available(iOS 26.0, *) else { throw XCTSkip("needs iOS 26") }
        let dir = try checkpoint
        let load = Date()
        try LayaDeviceJudge.preloadBlocking(dir)
        print("sparkcore: laya loaded in \(Int(Date().timeIntervalSince(load)))s")
        let engine = try SparkcoreEngine.make(settingsJSON: #"{"max_concurrent": 1, "question_seconds": 120}"#, judge: LayaDeviceJudge(directory: dir))
        let start = Date()
        let result = try object(try engine.check(idea, listener: nil))
        let answers = result["answers"] as? [[String: Any]] ?? []
        let failed = answers.filter { ($0["error"] as? String).map { !$0.hasPrefix("no ") } ?? false }
        let judged = answers.compactMap { $0["latency_ms"] as? Int }.filter { $0 > 0 }
        print("sparkcore: laya check \(result["verdict"] ?? "-") in \(String(format: "%.1f", Date().timeIntervalSince(start)))s, \(answers.count) answers, \(failed.count) failed: \(failed.map { "\($0["id"]!): \($0["error"]!)" })")
        print("sparkcore: \(judged.count) laya calls, mean \(judged.reduce(0, +) / max(judged.count, 1)) ms, slowest \(judged.max() ?? 0) ms")
        XCTAssertEqual(result["status"] as? String, "ok")
        XCTAssertEqual(result["backend"] as? String, "laya")
        XCTAssertNotNil(result["verdict"])
        XCTAssertTrue(failed.isEmpty)
    }
}
