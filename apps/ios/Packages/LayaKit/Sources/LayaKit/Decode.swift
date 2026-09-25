import Foundation

/// A decided answer, in laya-coreml's output semantics.
public struct LayaAnswer: Sendable, Equatable {
    public let kind: LayaQuestion.Kind
    /// Probability per label (choice labels, level indices, or false/true),
    /// calibrated, in label order.
    public let probabilities: [(label: String, p: Double)]
    /// choice: the most likely label.
    public let choice: String?
    /// score: the expected level index, Σ i·p(i).
    public let score: Double?
    /// noul: p(true).
    public let noul: Double?
    /// choice/score: 1 − H(p)/log k; noul: max(p, 1−p).
    public let confidence: Double
    /// Tokens the model read, and state tokens cut to fit (0 = none).
    public let tokens: Int
    public let droppedStateTokens: Int

    public static func == (a: LayaAnswer, b: LayaAnswer) -> Bool {
        a.kind == b.kind && a.choice == b.choice && a.score == b.score && a.noul == b.noul && a.confidence == b.confidence
            && a.probabilities.map(\.label) == b.probabilities.map(\.label) && a.probabilities.map(\.p) == b.probabilities.map(\.p)
    }

    /// The label the answer selects: the choice, the most likely level, or
    /// "true"/"false".
    public var selected: String {
        switch kind {
        case .choice: return choice ?? ""
        case .score: return probabilities.max { $0.p < $1.p }?.label ?? "0"
        case .noul: return (noul ?? 0) >= 0.5 ? "true" : "false"
        }
    }
}

/// Calibration: a softmax temperature per question type, overridden per
/// type-and-option-count bucket; every value clamped to [0.5, 5] as
/// laya-coreml (following upstream v0.3.5) does — the shipped choice:11+
/// bucket (0.10) would otherwise report a coin flip as a certainty.
public struct LayaCalibration: Sendable, Equatable {
    public static let range = 0.5...5.0

    public var byType: [Double]  // choice, score, noul
    public var byBucket: [String: Double]
    /// Buckets the clamp changed, for a caller that wants to say so.
    public var clamped: [String]

    public init(byType: [Double] = [1, 1, 1], byBucket: [String: Double] = [:]) {
        var clamped: [String] = []
        func clamp(_ t: Double, _ name: String) -> Double {
            guard t.isFinite else { return 1 }
            let c = min(Self.range.upperBound, max(Self.range.lowerBound, t))
            if c != t { clamped.append(name) }
            return c
        }
        self.byType = byType.enumerated().map { clamp($0.element, "temperature[\($0.offset)]") }
        self.byBucket = byBucket.reduce(into: [:]) { $0[$1.key] = clamp($1.value, $1.key) }
        self.clamped = clamped.sorted()
    }

    /// From rl_agent_config.json.
    public init(agentConfig: [String: Any]) throws {
        let raw = (agentConfig["temperature"] as? [NSNumber])?.map(\.doubleValue) ?? [1, 1, 1]
        let buckets = (agentConfig["temperature_by_options"] as? [String: NSNumber])?.mapValues(\.doubleValue) ?? [:]
        guard raw.count == 3, (raw + buckets.values).allSatisfy({ $0.isFinite && $0 > 0 }) else {
            throw LayaError.unsupported("calibration temperatures must be finite and positive")
        }
        self.init(byType: raw, byBucket: buckets)
    }

    static func bucket(_ kind: LayaQuestion.Kind, _ k: Int) -> String {
        let size = k <= 2 ? "2" : k <= 5 ? "3-5" : k <= 10 ? "6-10" : "11+"
        return "\(kind.rawValue):\(size)"
    }

    public func temperature(_ kind: LayaQuestion.Kind, options k: Int) -> Double {
        byBucket[Self.bucket(kind, k)] ?? byType[Int(kind.index)]
    }
}

enum Decode {
    /// Softmax of the first k logits at temperature t, computed as numpy
    /// does it in float32 (the Python runtime's dtype) and widened after.
    static func probabilities(_ logits: [Float], k: Int, temperature t: Double) -> [Double] {
        let z = logits.prefix(k).map { $0 / Float(t) }
        let top = z.max() ?? 0
        let e = z.map { expf($0 - top) }
        let sum = e.reduce(0, +)
        return e.map { Double($0 / sum) }
    }

    /// 1 − H(p)/log k, clipped to [0, 1]; 1 for a single option.
    static func confidence(_ p: [Double]) -> Double {
        let k = p.count
        guard k >= 2 else { return 1 }
        let h = -p.reduce(0) { $0 + $1 * log(min(max($1, 1e-12), 1)) }
        return min(max(1 - h / log(Double(k)), 0), 1)
    }

    static func answer(_ q: LayaQuestion, logits: [Float], sequence: LayaSequence, calibration: LayaCalibration) -> LayaAnswer {
        let k = sequence.markers.count
        let p = probabilities(logits, k: k, temperature: calibration.temperature(q.kind, options: k))
        let labels = q.labels
        let pairs = zip(labels, p).map { (label: $0, p: $1) }
        var choice: String?, score: Double?, noul: Double?
        var conf = confidence(p)
        switch q.kind {
        case .choice:
            // numpy's argmax: the first of equal maxima.
            var best = 0
            for i in p.indices where p[i] > p[best] { best = i }
            choice = labels[best]
        case .score:
            score = p.enumerated().reduce(0) { $0 + Double($1.offset) * $1.element }
        case .noul:
            noul = p[1]
            conf = max(p[1], 1 - p[1])
        }
        return LayaAnswer(
            kind: q.kind, probabilities: pairs, choice: choice, score: score, noul: noul, confidence: conf,
            tokens: sequence.ids.count, droppedStateTokens: sequence.droppedStateTokens)
    }
}
