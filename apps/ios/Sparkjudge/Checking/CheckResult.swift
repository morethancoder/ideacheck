import Foundation

// Codable mirror of schemas/check_result.schema.json. Property names are the
// schema's own (snake_case in CodingKeys), so a field added to the schema is a
// line here. Fields the schema requires are non-optional; the rest are optional.

struct CheckResult: Codable, Sendable, Hashable {
    var status: String
    var id: String
    var backend: String
    var writer: String?
    var model: String
    var method: String
    var ideaType: IdeaType?
    var missing: [Missing]
    var partial: Bool?
    var extracted: [String]?
    var research: ResearchReport?
    var answers: [Answer]
    var composite: Double
    var compositeConfidence: Double
    var verdict: String?
    var verdictReason: String?
    var dimensions: [Dimension]
    var topStrengths: [Contribution]
    var topRisks: [Contribution]
    var timing: Timing
    var costEstimateUsd: Double
    var cost: Cost
    var summary: String?
    var rubric: RubricRef?
    var configHash: String
    var warnings: [String]?
    var error: String?
    var createdAt: String

    enum CodingKeys: String, CodingKey {
        case status, id, backend, writer, model, method
        case ideaType = "idea_type"
        case missing, partial, extracted, research, answers, composite
        case compositeConfidence = "composite_confidence"
        case verdict
        case verdictReason = "verdict_reason"
        case dimensions
        case topStrengths = "top_strengths"
        case topRisks = "top_risks"
        case timing
        case costEstimateUsd = "cost_estimate_usd"
        case cost, summary, rubric
        case configHash = "config_hash"
        case warnings, error
        case createdAt = "created_at"
    }

    struct IdeaType: Codable, Sendable, Hashable {
        var choice: String
        var confidence: Double
    }

    struct Missing: Codable, Sendable, Hashable, Identifiable {
        var id: String
        var probability: Double
        var ask: String
        var fills: String
    }

    struct Answer: Codable, Sendable, Hashable, Identifiable {
        var id: String
        var kind: String
        var choice: String?
        var score: Double?
        var noul: Double?
        var probabilities: [String: Double]
        var confidence: Double
        var method: String
        var model: String
        var latencyMs: Int
        var tokensIn: Int?
        var tokensOut: Int?
        var tokensCached: Int?
        var costUsd: Double?
        var orderBias: Double?
        var error: String?

        enum CodingKeys: String, CodingKey {
            case id, kind, choice, score, noul, probabilities, confidence, method, model
            case latencyMs = "latency_ms"
            case tokensIn = "tokens_in"
            case tokensOut = "tokens_out"
            case tokensCached = "tokens_cached"
            case costUsd = "cost_usd"
            case orderBias = "order_bias"
            case error
        }
    }

    struct Dimension: Codable, Sendable, Hashable, Identifiable {
        var id: String
        /// Normalized [0,1] before polarity; `null` when the question was not scored.
        var value: Double?
        var weight: Double
        var polarity: Int
        var confidence: Double
        var error: String?

        /// The value read so that 1 is always good news.
        var goodness: Double? {
            guard let value else { return nil }
            return polarity < 0 ? 1 - value : value
        }

        func encode(to encoder: any Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(id, forKey: .id)
            try c.encode(value, forKey: .value) // explicit null, as the schema requires the key
            try c.encode(weight, forKey: .weight)
            try c.encode(polarity, forKey: .polarity)
            try c.encode(confidence, forKey: .confidence)
            try c.encodeIfPresent(error, forKey: .error)
        }
    }

    struct Contribution: Codable, Sendable, Hashable, Identifiable {
        var id: String
        var value: Double
        var weight: Double
    }

    struct Evidence: Codable, Sendable, Hashable {
        var topic: String
        var title: String
        var summary: String
        var url: String
        var relation: String?
        var confidence: Double?
        var used: Bool
    }

    struct Query: Codable, Sendable, Hashable {
        var topic: String
        var query: String
    }

    struct ResearchReport: Codable, Sendable, Hashable {
        var by: String
        var queries: [Query]?
        var model: String?
        var cached: Bool?
        var findings: [Evidence]
    }

    struct RubricRef: Codable, Sendable, Hashable {
        var name: String
        var hash: String
    }

    struct Timing: Codable, Sendable, Hashable {
        var totalMs: Int
        var slowestQuestion: String?

        enum CodingKeys: String, CodingKey {
            case totalMs = "total_ms"
            case slowestQuestion = "slowest_question"
        }
    }

    struct Price: Codable, Sendable, Hashable {
        var `in`: Double
        var out: Double
        var cachedIn: Double?

        enum CodingKeys: String, CodingKey {
            case `in`, out
            case cachedIn = "cached_in"
        }
    }

    struct Cost: Codable, Sendable, Hashable {
        var usd: Double
        var basis: String
        var model: String?
        var tokensIn: Int
        var tokensCached: Int?
        var tokensOut: Int
        var pricePerMtok: Price?
        var note: String?

        enum CodingKeys: String, CodingKey {
            case usd, basis, model
            case tokensIn = "tokens_in"
            case tokensCached = "tokens_cached"
            case tokensOut = "tokens_out"
            case pricePerMtok = "price_per_mtok"
            case note
        }
    }
}

extension CheckResult {
    static func decode(_ data: Data) throws -> CheckResult {
        try JSONDecoder().decode(CheckResult.self, from: data)
    }

    func encoded() throws -> Data {
        let e = JSONEncoder()
        e.outputFormatting = [.sortedKeys]
        return try e.encode(self)
    }

    /// The card's rating: composite × 10, one decimal.
    var rating: Double { Self.rating(composite) }

    static func rating(_ composite: Double) -> Double {
        (min(max(composite, 0), 1) * 100).rounded() / 10
    }

    var verdictValue: Verdict? { verdict.flatMap(Verdict.init(rawValue:)) }
}

/// The verdicts ideacheck's gates can reach. `uncertain` is what a rubric reports
/// when the composite confidence is below its `min_confidence`.
enum Verdict: String, Codable, Sendable, CaseIterable {
    case build, explore, park, kill, uncertain

    var word: String { rawValue.capitalized }

    var blurb: String {
        switch self {
        case .build: "Worth building now"
        case .explore: "Worth a closer look"
        case .park: "Keep it, not now"
        case .kill: "Let it go"
        case .uncertain: "Not enough to go on"
        }
    }

    var symbol: String {
        switch self {
        case .build: "hammer.fill"
        case .explore: "binoculars.fill"
        case .park: "parkingsign"
        case .kill: "xmark.octagon.fill"
        case .uncertain: "questionmark.circle.fill"
        }
    }

    /// The thresholds every shipped rubric uses (composite in [0,1]).
    static func from(composite: Double) -> Verdict {
        switch composite {
        case 0.70...: .build
        case 0.50..<0.70: .explore
        case 0.30..<0.50: .park
        default: .kill
        }
    }
}
