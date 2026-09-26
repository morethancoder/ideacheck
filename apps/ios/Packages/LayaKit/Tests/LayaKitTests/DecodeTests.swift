import Foundation
import Testing
@testable import LayaKit

/// The arithmetic after the model: laya-coreml's system_one on made-up logits.
struct DecodeTests {
    func sequence(_ kind: LayaQuestion.Kind, options k: Int) -> LayaSequence {
        LayaSequence(ids: [1, 2, 3], markers: (0..<k).map { Int32($0 + 1) }, kind: kind, droppedStateTokens: 0, stateTokens: 0)
    }

    @Test func softmaxAtTemperature() {
        let p = Decode.probabilities([2, 1, 0, 99], k: 3, temperature: 2)
        // exp(1), exp(.5), exp(0) normalized; the fourth slot is not an option.
        let e = [exp(1.0), exp(0.5), 1.0]
        let sum = e.reduce(0, +)
        for (a, b) in zip(p, e.map { $0 / sum }) { #expect(abs(a - b) < 1e-6) }
        #expect(p.count == 3)
    }

    @Test func confidenceIsOneMinusNormalizedEntropy() {
        #expect(abs(Decode.confidence([0.5, 0.5])) < 1e-9)
        #expect(abs(Decode.confidence([1, 0]) - 1) < 1e-6)
        #expect(Decode.confidence([1]) == 1)
        let p = [0.7, 0.2, 0.1]
        let h = -p.reduce(0) { $0 + $1 * log($1) }
        #expect(abs(Decode.confidence(p) - (1 - h / log(3))) < 1e-9)
    }

    @Test func calibrationBucketsAndClamp() throws {
        let cal = try LayaCalibration(agentConfig: [
            "temperature": [1.01, 1.03, 1.05],
            "temperature_by_options": ["choice:3-5": 1.76, "choice:11+": 0.1006, "noul:2": 1.98, "score:6-10": 9.0],
        ])
        #expect(cal.temperature(.choice, options: 4) == 1.76)
        #expect(cal.temperature(.choice, options: 2) == 1.01)  // no choice:2 bucket: the type's
        #expect(cal.temperature(.choice, options: 12) == 0.5)  // 0.1006 clamped up
        #expect(cal.temperature(.score, options: 7) == 5)  // 9 clamped down
        #expect(cal.temperature(.noul, options: 2) == 1.98)
        #expect(cal.clamped == ["choice:11+", "score:6-10"])
        #expect(throws: LayaError.self) { try LayaCalibration(agentConfig: ["temperature": [1, 0, 1]]) }
    }

    @Test func choicePicksTheFirstLargest() {
        let q = LayaQuestion.choice("Which?", options: [("a", "one"), ("b", "two"), ("c", "three")])
        let a = Decode.answer(q, logits: [1, 3, 3, 0], sequence: sequence(.choice, options: 3), calibration: .init())
        #expect(a.choice == "b")
        #expect(a.probabilities.map(\.label) == ["a", "b", "c"])
        #expect(abs(a.probabilities.map(\.p).reduce(0, +) - 1) < 1e-6)
        #expect(a.selected == "b")
    }

    @Test func scoreIsTheExpectedLevel() {
        let q = LayaQuestion.score("How?", levels: ["low", "mid", "high"])
        let a = Decode.answer(q, logits: [0, 0, log(2)], sequence: sequence(.score, options: 3), calibration: .init())
        // p = [.25, .25, .5] → 0·.25 + 1·.25 + 2·.5 = 1.25
        #expect(abs((a.score ?? 0) - 1.25) < 1e-6)
        #expect(a.selected == "2")
    }

    @Test func noulIsPTrueWithMaxConfidence() {
        let q = LayaQuestion.noul("Is it?")
        let a = Decode.answer(q, logits: [0, log(3)], sequence: sequence(.noul, options: 2), calibration: .init())
        #expect(abs((a.noul ?? 0) - 0.75) < 1e-6)
        #expect(abs(a.confidence - 0.75) < 1e-6)
        #expect(a.selected == "true")
        #expect(a.probabilities.map(\.label) == ["false", "true"])
    }

    @Test func optionTextsFollowLaya() throws {
        #expect(LayaQuestion.noul("x").optionTexts == ["false: no, the statement does not hold", "true: yes, the statement holds"])
        #expect(LayaQuestion.noul("x", yes: "named", no: "").optionTexts == ["false: no, the statement does not hold", "true: named"])
        #expect(LayaQuestion.score("x", levels: ["a", "b"]).optionTexts == ["level 0: a", "level 1: b"])
        let structured = try LayaQuestion(wire: JSONValue(parsing: #"{"type":"choice","instructions":{"task":"t"},"criteria":{"billing":{"description":"refunds"},"other":false,"none":null,"empty":""}}"#))
        #expect(structured.optionTexts == [#"billing: {"description": "refunds"}"#, "other: false", "none", "empty"])
        #expect(structured.instructionText == #"{"task": "t"}"#)
        let listed = try LayaQuestion(wire: JSONValue(parsing: #"{"type":"choice","instructions":"q","criteria":["a","b"]}"#))
        #expect(listed.labels == ["a", "b"])
        #expect(throws: LayaError.self) { try LayaQuestion(wire: JSONValue(parsing: #"{"type":"choice","instructions":"q","criteria":["a","a"]}"#)) }
        #expect(throws: LayaError.self) { try LayaQuestion(wire: JSONValue(parsing: #"{"type":"vote","instructions":"q"}"#)) }
    }
}

/// The state is read as Python's json.dumps writes it, so its text must match.
struct JSONValueTests {
    @Test func dumpsLikePython() throws {
        let v = try JSONValue(parsing: #"{"b":1,"a":[true,null,2.5,"x\"y\\z\n\u0001é🚀/"],"c":{}}"#)
        #expect(v.pythonJSON == #"{"b": 1, "a": [true, null, 2.5, "x\"y\\z\n\u0001é🚀/"], "c": {}}"#)
        let u = #"\"# + "u"  // spelled out, so the escapes stay escapes
        let escaped = u + "0001" + u + "00e9" + u + "d83d" + u + "de80"
        #expect(v.pythonJSON(ensureASCII: true) == #"{"b": 1, "a": [true, null, 2.5, "x\"y\\z\n"# + escaped + #"/"], "c": {}}"#)
    }

    @Test func keepsKeyOrderAndSurrogates() throws {
        let v = try JSONValue(parsing: #"{"z":"🚀","y":[],"x":"a"}"#)
        #expect(v.objectValue?.map(\.0) == ["z", "y", "x"])
        #expect(v["z"]?.stringValue == "🚀")
        #expect(throws: JSONValue.ParseError.self) { try JSONValue(parsing: "{\"a\":}") }
        #expect(throws: JSONValue.ParseError.self) { try JSONValue(parsing: "[1] 2") }
    }
}
