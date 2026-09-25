import Foundation

/// Answers typed questions over a state with a loaded checkpoint, with the
/// semantics of laya-coreml's `Agent.predict`: one forward pass per question,
/// calibrated probabilities, no sampling and no generated text.
///
/// A state longer than the checkpoint's budget is cut from the end to fit, as
/// upstream does; each answer says how many state tokens it lost
/// (`droppedStateTokens`), so a caller can tell a trimmed read from a whole
/// one. A question whose own text and options do not fit is an error.
public final class LayaJudge: Sendable {
    public let model: LayaModel

    public init(model: LayaModel) {
        self.model = model
    }

    /// One answer per question, in the order given.
    public func evaluate(state: LayaState, questions: [(id: String, question: LayaQuestion)]) throws -> [(id: String, answer: LayaAnswer)] {
        try questions.map { ($0.id, try evaluate(state: state, question: $0.question)) }
    }

    public func evaluate(state: LayaState, question: LayaQuestion) throws -> LayaAnswer {
        let seq = try model.sequence(state: state, question: question)
        let logits = try model.logits(seq)
        return Decode.answer(question, logits: logits, sequence: seq, calibration: model.calibration)
    }

    /// laya-coreml's request shape: questions as {"id": {"type",
    /// "instructions", "criteria"}}, in the order they are written.
    public func evaluate(state: LayaState, questions wire: JSONValue) throws -> [(id: String, answer: LayaAnswer)] {
        guard let pairs = wire.objectValue else { throw LayaError.badQuestion("questions must be an object keyed by id") }
        return try evaluate(state: state, questions: pairs.map { ($0.0, try LayaQuestion(wire: $0.1)) })
    }
}

extension LayaAnswer {
    /// laya-coreml's answer object (without the action head), probabilities
    /// rounded to 4 places as it rounds them.
    public var wire: JSONValue {
        func num(_ x: Double) -> JSONValue { .number(String((x * 10000).rounded() / 10000)) }
        var o: [(String, JSONValue)] = [("type", .string(kind.rawValue)), ("confidence", num(confidence))]
        let probs = JSONValue.object(probabilities.map { ($0.label, num($0.p)) })
        switch kind {
        case .choice: o += [("choice", .string(choice ?? "")), ("probabilities", probs)]
        case .score: o += [("score", num(score ?? 0)), ("probabilities", probs)]
        case .noul: o += [("noul", num(noul ?? 0))]
        }
        return .object(o)
    }
}
