import Foundation

/// Sparkcore's progress events (`Listener` in mobile/bridge.go, the core's
/// `ideacheck.Event`) as the app's `CheckProgress`: the same JSON a server
/// streams as SSE `progress`.
enum SparkcoreEvent {
    /// nil for JSON that is not an event. An answer that does not fit
    /// `CheckResult.Answer` (the extract and summary steps carry partial
    /// ones) is dropped; the step still counts.
    static func progress(_ json: String) -> CheckProgress? {
        try? JSONDecoder().decode(Wire.self, from: Data(json.utf8)).progress
    }

    private struct Wire: Decodable {
        var progress: CheckProgress

        enum CodingKeys: String, CodingKey { case type, stage, question, answer, value }

        init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            progress = CheckProgress(
                type: try c.decode(String.self, forKey: .type),
                stage: try c.decode(String.self, forKey: .stage),
                question: try c.decode(CheckQuestion.self, forKey: .question),
                answer: try? c.decodeIfPresent(CheckResult.Answer.self, forKey: .answer),
                value: try? c.decodeIfPresent(Double.self, forKey: .value))
        }
    }
}

/// How many questions a check's scoring stage asks, worked out from the
/// core's own rubrics (`sparkcoreRubrics()`): the chosen or routed rubric's
/// questions, less the ones a check on this phone skips — `requires:
/// [evidence]` (nothing is searched here) and `requires: [profile]` when no
/// profile was sent. The rubric is the one asked for; else the router's
/// answer when it names a rubric; else the one rubric holding every question
/// scored so far.
struct ScorePlan: Sendable {
    struct Question: Sendable, Hashable {
        var id: String
        var requires: [String]
    }

    let rubrics: [String: [Question]]
    private(set) var rubric: String?
    let hasProfile: Bool
    private var seen: Set<String> = []

    init(rubrics: [String: [Question]], rubric: String?, hasProfile: Bool) {
        self.rubrics = rubrics
        self.rubric = rubric.flatMap { rubrics[$0] == nil ? nil : $0 }
        self.hasProfile = hasProfile
    }

    init(rubricsJSON: String, rubric: String?, hasProfile: Bool) {
        self.init(rubrics: Self.parse(rubricsJSON), rubric: rubric, hasProfile: hasProfile)
    }

    static func parse(_ json: String) -> [String: [Question]] {
        struct R: Decodable {
            struct Q: Decodable {
                var id: String
                var requires: [String]?
            }
            var questions: [Q]
        }
        guard let all = try? JSONDecoder().decode([String: R].self, from: Data(json.utf8)) else { return [:] }
        return all.mapValues { $0.questions.map { Question(id: $0.id, requires: $0.requires ?? []) } }
    }

    /// The questions the chosen rubric asks here; nil while it is not known.
    var expected: Int? {
        guard let rubric, let qs = rubrics[rubric] else { return nil }
        return qs.filter { q in
            !q.requires.contains("evidence") && !(q.requires.contains("profile") && !hasProfile)
        }.count
    }

    /// Reads one event and returns `expected` after it.
    mutating func see(_ p: CheckProgress) -> Int? {
        if rubric == nil {
            if p.stage == "preflight", p.isFinished, let choice = p.answer?.choice, rubrics[choice] != nil {
                rubric = choice // the router's answer, when it names a rubric
            } else if p.stage == "score" {
                seen.insert(p.question.id)
                let holding = rubrics.filter { _, qs in seen.isSubset(of: Set(qs.map(\.id))) }
                if holding.count == 1 { rubric = holding.first?.key }
            }
        }
        return expected
    }
}

/// Live progress of one running check, for the card and detail views: the
/// stage in words ("Reading your idea", "Scoring 3 of 12", "Summarising")
/// and a fraction for a progress bar.
struct CheckRun: Sendable, Equatable {
    var started = 0
    var finished = 0
    var stage = "preflight"
    var lastQuestion: String?
    /// The current stage's own counts.
    var stageStarted = 0
    var stageFinished = 0
    /// Score-stage questions answered (derived ones included), and how many
    /// the stage asks when the checker knows (`CheckProgress.expected`).
    var scored = 0
    var scoreTotal: Int?

    mutating func apply(_ p: CheckProgress) {
        if p.stage != stage {
            stage = p.stage
            stageStarted = 0
            stageFinished = 0
        }
        if p.isFinished {
            finished += 1
            stageFinished += 1
            if p.stage == "score" { scored += 1 }
        } else {
            started += 1
            stageStarted += 1
            if p.stage == "preflight" || p.stage == "score" { lastQuestion = p.question.id }
        }
        if p.stage == "score", let n = p.expected { scoreTotal = n }
    }

    /// The stage, in words.
    var label: String {
        switch stage {
        case "load": "Loading the judge"
        case "extract", "preflight": "Reading your idea"
        case "research": "Looking it up"
        case "score":
            if let total = scoreTotal, total > 0 { "Scoring \(min(scored + 1, total)) of \(total)" } else { "Scoring" }
        case "explain": "Summarising"
        default: "Checking"
        }
    }

    /// The question being asked, while one is (reading and scoring).
    var question: String? {
        stage == "preflight" || stage == "score" ? lastQuestion : nil
    }

    /// A rough fraction for a progress bar: each stage owns a stretch of it,
    /// scoring the longest.
    var fraction: Double {
        let r = stageStarted == 0 ? 0 : Double(stageFinished) / Double(max(stageStarted, stageFinished))
        switch stage {
        case "load": return 0.03
        case "extract": return 0.06
        case "preflight": return 0.1 + 0.15 * r
        case "research": return 0.25 + 0.1 * r
        case "score":
            let s = scoreTotal.map { Double(scored) / Double(max($0, 1)) } ?? r
            return 0.35 + 0.55 * min(s, 1)
        case "explain": return 0.93
        default: return 0.05
        }
    }
}
