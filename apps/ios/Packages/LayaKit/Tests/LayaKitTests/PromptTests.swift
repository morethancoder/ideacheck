import Foundation
import Testing
@testable import LayaKit

/// The model's input, token for token, against laya-coreml's build_sequence
/// (Fixtures/sequences.json, scripts/fixtures.py): laya's validation cases —
/// many languages, an empty and a 200-sentence state, [MASK] literals,
/// structured criteria, twenty options — and ideacheck's own questions over
/// states shaped as the core sends them.
struct PromptTests {
    static let checkpoint = "laya-typed-decisions-coreml"

    @Test func sequencesMatchPython() throws {
        try #require(Local.hasTokenizer(Self.checkpoint), "no tokenizer: scripts/fetch-model.sh --tokenizer aac6fef/\(Self.checkpoint)")
        let tok = try Local.tokenizer(Self.checkpoint)
        // laya-typed-decisions' rl_agent_config.json: max_len 1024, head_max_len 256.
        var checked = 0
        for c in try Fixture.json("sequences.json").arrayValue ?? [] {
            let name = c["name"]?.stringValue ?? "?"
            let state = LayaState.json(c["state"] ?? .null)
            let questions = c["questions"]?.objectValue ?? []
            let want = c["sequences"]?.arrayValue ?? []
            try #require(questions.count == want.count)
            for ((qid, wire), expected) in zip(questions, want) {
                let seq = try tok.sequence(state: state, question: LayaQuestion(wire: wire), maxLen: 1024, headMaxLen: 256)
                #expect(seq.ids.map(Int.init) == (expected["ids"]?.arrayValue ?? []).compactMap(\.intValue), "\(name)/\(qid) ids")
                #expect(seq.markers.map(Int.init) == (expected["markers"]?.arrayValue ?? []).compactMap(\.intValue), "\(name)/\(qid) markers")
                #expect(Int(seq.kind.index) == expected["qtype"]?.intValue, "\(name)/\(qid) qtype")
                checked += 1
            }
        }
        #expect(checked >= 60)
    }

    /// A state past the budget is cut from the end and the cut is counted;
    /// the question itself is never cut.
    @Test func longStateIsTrimmedAndReported() throws {
        try #require(Local.hasTokenizer(Self.checkpoint))
        let tok = try Local.tokenizer(Self.checkpoint)
        let q = LayaQuestion.noul("Does the customer ask for money back?")
        let long = LayaState.text(String(repeating: "The customer reports duplicate billing. ", count: 300))
        let seq = try tok.sequence(state: long, question: q, maxLen: 256, headMaxLen: 256)
        #expect(seq.ids.count == 256)
        #expect(seq.droppedStateTokens > 0)
        #expect(seq.ids.last == Int32(tok.sepID))
        let short = try tok.sequence(state: .text("Refund please."), question: q, maxLen: 256, headMaxLen: 256)
        #expect(short.droppedStateTokens == 0)
        // Every state token either made it in or was counted as dropped.
        let prefix = short.ids.count - short.stateTokens - 1
        #expect(seq.stateTokens - seq.droppedStateTokens == 256 - prefix - 1)
        #expect(short.markers.count == 2)
    }
}
