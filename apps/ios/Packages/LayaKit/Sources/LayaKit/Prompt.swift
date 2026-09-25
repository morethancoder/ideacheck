import Foundation

/// One typed question in Laya's (and System One's) schema.
public struct LayaQuestion: Sendable, Equatable {
    public enum Kind: String, Sendable {
        case choice, score, noul

        /// The model's question-type input.
        var index: Int32 {
            switch self {
            case .choice: 0
            case .score: 1
            case .noul: 2
            }
        }
    }

    public let kind: Kind
    /// Usually a string; anything else is read as its JSON text.
    public let instructions: JSONValue
    /// choice: label → description (a description may be null or "" for
    /// none); score: the ordered levels, lowest first; noul: optional
    /// {"true", "false"} descriptions.
    public let criteria: JSONValue?

    public init(kind: Kind, instructions: JSONValue, criteria: JSONValue? = nil) {
        self.kind = kind
        self.instructions = instructions
        self.criteria = criteria
    }

    public static func choice(_ instructions: String, options: [(String, String)]) -> LayaQuestion {
        LayaQuestion(kind: .choice, instructions: .string(instructions), criteria: .object(options.map { ($0.0, .string($0.1)) }))
    }

    public static func score(_ instructions: String, levels: [String]) -> LayaQuestion {
        LayaQuestion(kind: .score, instructions: .string(instructions), criteria: .array(levels.map(JSONValue.string)))
    }

    public static func noul(_ instructions: String, yes: String? = nil, no: String? = nil) -> LayaQuestion {
        var crit: [(String, JSONValue)] = []
        if let yes { crit.append(("true", .string(yes))) }
        if let no { crit.append(("false", .string(no))) }
        return LayaQuestion(kind: .noul, instructions: .string(instructions), criteria: crit.isEmpty ? nil : .object(crit))
    }

    /// A question in the wire shape: {"type", "instructions", "criteria"}.
    /// A choice may list its labels as an array, as laya-coreml accepts.
    public init(wire: JSONValue) throws {
        guard let type = wire["type"]?.stringValue, let kind = Kind(rawValue: type) else {
            throw LayaError.badQuestion("unknown question type; expected choice, score, or noul")
        }
        guard let ins = wire["instructions"] else { throw LayaError.badQuestion("question is missing instructions") }
        var crit = wire["criteria"]
        if crit == .null { crit = nil }
        switch kind {
        case .choice:
            if let labels = crit?.arrayValue {
                let names = labels.compactMap(\.stringValue)
                guard names.count == labels.count, Set(names).count == names.count else {
                    throw LayaError.badQuestion("choice labels must be unique strings")
                }
                crit = .object(names.map { ($0, .null) })
            }
            guard let o = crit?.objectValue, !o.isEmpty else { throw LayaError.badQuestion("choice criteria must be a nonempty dictionary or list") }
        case .score:
            guard let a = crit?.arrayValue, !a.isEmpty else { throw LayaError.badQuestion("score criteria must be a nonempty list") }
        case .noul:
            if let c = crit, c.objectValue == nil { throw LayaError.badQuestion("noul criteria must be a dictionary with false/true descriptions") }
        }
        self.init(kind: kind, instructions: ins, criteria: crit)
    }

    /// The labels answers are keyed by: choice labels, level indices, or
    /// ["false", "true"].
    public var labels: [String] {
        switch kind {
        case .choice: criteria?.objectValue?.map(\.0) ?? []
        case .score: (criteria?.arrayValue ?? []).indices.map(String.init)
        case .noul: ["false", "true"]
        }
    }

    var instructionText: String {
        if case .string(let s) = instructions { return s }
        return instructions.pythonJSON(ensureASCII: true)
    }

    /// One criterion as text: strings as they are, anything else as JSON.
    static func render(_ v: JSONValue) -> String {
        if case .string(let s) = v { return s }
        return v.pythonJSON
    }

    /// The option texts in label order (laya's render_options).
    var optionTexts: [String] {
        switch kind {
        case .choice:
            return (criteria?.objectValue ?? []).map { key, value in
                if value == .null || value == .string("") { return key }
                return "\(key): \(Self.render(value))"
            }
        case .score:
            return (criteria?.arrayValue ?? []).enumerated().map { "level \($0.offset): \(Self.render($0.element))" }
        case .noul:
            let f = criteria?["false"], t = criteria?["true"]
            func given(_ v: JSONValue?) -> Bool { v != nil && v != .null && v != .string("") }
            return [
                "false: " + (given(f) ? Self.render(f!) : "no, the statement does not hold"),
                "true: " + (given(t) ? Self.render(t!) : "yes, the statement holds"),
            ]
        }
    }
}

/// What the model reads: a state as text, or structured (serialized the way
/// Python's json.dumps does).
public enum LayaState: Sendable {
    case text(String)
    case json(JSONValue)

    var serialized: String {
        switch self {
        case .text(let s): s
        case .json(.string(let s)): s
        case .json(let v): v.pythonJSON
        }
    }
}

/// One question laid out as the model's input sequence.
public struct LayaSequence: Sendable, Equatable {
    public var ids: [Int32]
    /// Where each option's [MASK] sits, in label order.
    public var markers: [Int32]
    public var kind: LayaQuestion.Kind
    /// State tokens that did not fit and were cut from the end.
    public var droppedStateTokens: Int
    public var stateTokens: Int
}

extension Tokenizer {
    /// laya's build_prefix: [CLS] "<type> question: <instructions>" [SEP]
    /// [MASK] opt0 [MASK] opt1 … [SEP]. Each option keeps at most 48 tokens;
    /// the instructions get what is left of headMaxLen.
    func prefix(_ q: LayaQuestion, headMaxLen: Int) -> (ids: [Int], markers: [Int]) {
        let ins = q.instructionText.replacingOccurrences(of: maskToken, with: " ")
        var head = encode("\(q.kind.rawValue) question: \(ins)")
        var opts = q.optionTexts.map { [maskID] + encode(" " + $0.replacingOccurrences(of: maskToken, with: " ")).prefix(48) }
        var budget = headMaxLen - opts.reduce(0) { $0 + $1.count }
        if budget < 16 {
            let per = max(4, (headMaxLen - 16) / max(1, opts.count))
            opts = opts.map { Array($0.prefix(per)) }
            budget = headMaxLen - opts.reduce(0) { $0 + $1.count }
        }
        head = Array(head.prefix(max(8, budget)))
        var ids = [clsID] + head + [sepID]
        var markers: [Int] = []
        for o in opts {
            markers.append(ids.count)
            ids += o
        }
        ids.append(sepID)
        return (ids, markers)
    }

    /// laya's build_sequence: the prefix, then as much of the state as fits
    /// in maxLen (cut from the end), then [SEP].
    public func sequence(state: LayaState, question q: LayaQuestion, maxLen: Int, headMaxLen: Int) throws -> LayaSequence {
        let (prefix, markers) = self.prefix(q, headMaxLen: headMaxLen)
        let room = max(0, maxLen - prefix.count - 1)
        let st = encode(state.serialized.replacingOccurrences(of: maskToken, with: " "))
        var ids = prefix + st.prefix(room) + [sepID]
        ids = Array(ids.prefix(maxLen))
        let kept = markers.filter { $0 < maxLen }
        guard kept.count == q.optionTexts.count else {
            throw LayaError.capacity("the question has too many options for the token budget")
        }
        return LayaSequence(
            ids: ids.map(Int32.init), markers: kept.map(Int32.init), kind: q.kind,
            droppedStateTokens: max(0, st.count - room), stateTokens: st.count)
    }
}
