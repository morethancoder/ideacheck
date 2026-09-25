import Foundation

/// A checker that judges nothing: it returns a realistic result, the same for
/// the same idea every time, built on a real engine result
/// (`Resources/SampleResult.json`, from `ideacheck -b mock --agent`). For SwiftUI
/// previews, tests, screenshots and trying the app with no server.
struct PreviewChecker: IdeaChecker {
    /// Pause between progress events, so the UI has something to animate.
    var step: Duration = .milliseconds(90)

    var kind: CheckerKind { .preview }

    func check(_ intake: Intake, rubric: String?) -> AsyncThrowingStream<CheckEvent, any Error> {
        let step = self.step
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    let result = try Self.result(for: intake, rubric: rubric)
                    continuation.yield(.accepted(id: result.id))
                    let questions = result.dimensions.map { CheckQuestion(id: $0.id, kind: "noul", weight: $0.weight, polarity: $0.polarity) }
                    for (q, d) in zip(questions, result.dimensions) {
                        try await Task.sleep(for: step)
                        continuation.yield(.progress(CheckProgress(type: "started", stage: "score", question: q)))
                        try await Task.sleep(for: step)
                        continuation.yield(.progress(CheckProgress(type: d.value == nil ? "failed" : "answered", stage: "score", question: q, value: d.value)))
                    }
                    continuation.yield(.result(result, raw: try result.encoded()))
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    /// The questions each shipped rubric weighs (configs/rubrics/*.yaml when this
    /// was written) — only so a preview card has plausible rows.
    static let sketch: [IdeaCategory: [(id: String, weight: Double, polarity: Int)]] = [
        .business: [("problem_acuity", 2, 1), ("sisp", 1.5, -1), ("audience_specificity", 1, 1), ("market_size", 1, 1),
                    ("tarpit", 1.5, -1), ("differentiating_insight", 1.5, 1), ("why_now", 1, 1), ("scalability", 1, 1),
                    ("founder_market_fit", 2, 1), ("clarity", 0.5, 1)],
        .sideProject: [("personal_usefulness", 1.5, 1), ("would_use_weekly", 1, 1), ("learning_value", 1, 1),
                       ("scope_fits_time", 1.5, 1), ("first_version_defined", 1, 1), ("novelty_to_builder", 0.5, 1),
                       ("existing_tool_suffices", 1, -1), ("clarity", 0.5, 1)],
        .content: [("audience_specificity", 1.5, 1), ("distribution_named", 1.5, 1), ("sustainable_cadence", 1, 1),
                   ("creator_credibility", 1.5, 1), ("differentiated_angle", 1.5, 1), ("audience_value", 1, 1),
                   ("topic_depth", 0.5, 1), ("clarity", 0.5, 1)],
        .research: [("falsifiable", 2, 1), ("novelty_stated", 1.5, 1), ("still_open", 1, 1), ("feasibility", 1.5, 1),
                    ("method_named", 1, 1), ("potential_impact", 1.5, 1), ("researcher_fit", 1, 1), ("clarity", 0.5, 1)],
        .creative: [("originality", 1.5, 1), ("hook_strength", 1.5, 1), ("audience_named", 1, 1), ("scope_realism", 1.5, 1),
                    ("creator_motivation", 1, 1), ("craft_fit", 1, 1), ("clarity", 0.5, 1)],
    ]

    static func template() throws -> CheckResult {
        guard let url = Bundle.main.url(forResource: "SampleResult", withExtension: "json") else {
            throw CheckError.unavailable("The preview sample is missing from the app bundle.")
        }
        return try CheckResult.decode(Data(contentsOf: url))
    }

    /// A deterministic result for this intake.
    static func result(for intake: Intake, rubric: String?, template: CheckResult? = nil) throws -> CheckResult {
        var r = try template ?? Self.template()
        let key = intake.idea + "|" + (intake.fields ?? [:]).sorted { $0.key < $1.key }.map { "\($0.key)=\($0.value)" }.joined(separator: "|")
        var rng = SplitMix64(seed: FNV1a.hash(key))

        let category: IdeaCategory = {
            if let rubric, let c = IdeaCategory(rawValue: rubric), c != .other { return c }
            return IdeaCategory.displayOrder.dropLast()[Int(rng.next() % 5)]
        }()
        // A base quality for the idea, with each question scattered around it.
        let base = 0.25 + rng.unit() * 0.6
        let hasProfile = !(intake.profile ?? [:]).isEmpty
        var dims: [CheckResult.Dimension] = []
        for q in sketch[category] ?? [] {
            if !hasProfile, ["founder_market_fit", "scope_fits_time", "researcher_fit", "creator_credibility", "craft_fit"].contains(q.id) {
                dims.append(.init(id: q.id, value: nil, weight: q.weight, polarity: q.polarity, confidence: 0, error: "no profile given"))
                continue
            }
            let good = min(max(base + (rng.unit() - 0.5) * 0.5, 0.02), 0.98)
            let value = q.polarity < 0 ? 1 - good : good
            dims.append(.init(id: q.id, value: value, weight: q.weight, polarity: q.polarity, confidence: 0.35 + rng.unit() * 0.6))
        }
        let answered = dims.filter { $0.value != nil && $0.weight > 0 }
        let totalWeight = answered.reduce(0) { $0 + $1.weight }
        let composite = totalWeight == 0 ? 0 : answered.reduce(0) { $0 + ($1.goodness ?? 0) * $1.weight } / totalWeight
        let byGoodness = answered.sorted { ($0.goodness ?? 0) > ($1.goodness ?? 0) }
        let verdict = Verdict.from(composite: composite)

        r.id = String(format: "prv_%016llX", FNV1a.hash(key))
        r.backend = "preview"
        r.writer = "preview"
        r.model = "preview"
        r.method = "preview"
        r.ideaType = .init(choice: category.rawValue, confidence: 0.6 + rng.unit() * 0.35)
        r.answers = []
        r.dimensions = dims
        r.composite = composite
        r.compositeConfidence = answered.isEmpty ? 0 : answered.reduce(0) { $0 + $1.confidence } / Double(answered.count)
        r.verdict = verdict.rawValue
        r.verdictReason = String(format: "Composite %.2f meets the %@ threshold %.2f", composite, verdict.rawValue,
                                 [Verdict.build: 0.7, .explore: 0.5, .park: 0.3][verdict] ?? 0.0)
        if verdict == .kill { r.verdictReason = String(format: "Composite %.2f is below the park threshold 0.30", composite) }
        r.topStrengths = byGoodness.prefix(3).map { .init(id: $0.id, value: $0.goodness ?? 0, weight: $0.weight) }
        r.topRisks = byGoodness.reversed().prefix(3).map { .init(id: $0.id, value: $0.goodness ?? 0, weight: $0.weight) }
        r.missing = Self.missing(for: intake)
        r.partial = !r.missing.isEmpty
        r.rubric = .init(name: category.rawValue, hash: "preview")
        r.research = Self.research(for: intake)
        r.summary = Self.summary(title: intake.fields?["title"] ?? String(intake.idea.prefix(40)), verdict: verdict,
                                 strength: byGoodness.first?.id, risk: byGoodness.last?.id)
        r.warnings = ["Preview result: nothing was judged. Connect an ideacheck server in Settings for a real check."]
        r.cost = .init(usd: 0, basis: "none", model: "preview", tokensIn: 0, tokensOut: 0, note: "preview result, nothing was sent")
        r.costEstimateUsd = 0
        r.timing = .init(totalMs: 900 + Int(rng.next() % 900), slowestQuestion: dims.first?.id)
        r.createdAt = ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: 1_790_000_000))
        return r
    }

    private static func missing(for intake: Intake) -> [CheckResult.Missing] {
        let given = intake.fields ?? [:]
        let asks: [(String, String)] = [
            ("why_now", "What changed recently that makes this possible or needed now?"),
            ("competitors_known", "What do people use today instead?"),
            ("audience", "Who are the first ten people who would use it?"),
        ]
        return asks.filter { given[$0.0] == nil }.prefix(2).map {
            .init(id: "has_" + $0.0, probability: 0.3, ask: $0.1, fills: $0.0)
        }
    }

    private static func research(for intake: Intake) -> CheckResult.ResearchReport {
        let q = intake.fields?["title"] ?? String(intake.idea.prefix(60))
        let encoded = q.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? ""
        return .init(by: "preview", queries: [.init(topic: "competitors", query: q + " alternatives")], model: "preview", cached: false, findings: [
            .init(topic: "competitors", title: "Search: tools like this", summary: "A preview has no web access; this link runs the search the check would start with.",
                  url: "https://duckduckgo.com/?q=\(encoded)+alternatives", relation: "context", confidence: nil, used: false),
        ])
    }

    private static func summary(title: String, verdict: Verdict, strength: String?, risk: String?) -> String {
        let s = strength.map(FieldSpec.humanize) ?? "the idea itself"
        let k = risk.map(FieldSpec.humanize) ?? "the unknowns"
        return "“\(title)” reads as \(verdict.blurb.lowercased()). Its strongest side is \(s.lowercased()); the weakest is \(k.lowercased()). This is a preview made on the phone — run a real check to know more."
    }
}

/// 64-bit FNV-1a: small, stable across launches and platforms (unlike `Hasher`).
enum FNV1a {
    static func hash(_ string: String) -> UInt64 { hash(Array(string.utf8)) }

    static func hash<S: Sequence>(_ bytes: S) -> UInt64 where S.Element == UInt8 {
        var h: UInt64 = 0xcbf2_9ce4_8422_2325
        for b in bytes {
            h ^= UInt64(b)
            h &*= 0x0000_0100_0000_01B3
        }
        return h
    }
}

/// SplitMix64: a tiny deterministic generator, so a seed always rolls the same values.
struct SplitMix64: RandomNumberGenerator, Sendable {
    private var state: UInt64

    init(seed: UInt64) { state = seed }

    mutating func next() -> UInt64 {
        state &+= 0x9E37_79B9_7F4A_7C15
        var z = state
        z = (z ^ (z >> 30)) &* 0xBF58_476D_1CE4_E5B9
        z = (z ^ (z >> 27)) &* 0x94D0_49BB_1331_11EB
        return z ^ (z >> 31)
    }

    /// Uniform in [0, 1).
    mutating func unit() -> Double { Double(next() >> 11) / Double(1 << 53) }
}
