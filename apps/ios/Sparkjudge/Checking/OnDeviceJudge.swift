import Foundation
import LayaKit
import Sparkcore

/// Which model answers a check's typed questions on this iPhone.
enum OnDeviceJudge: Hashable, Sendable {
    /// A downloaded Laya checkpoint on Core ML: milliseconds a question on a
    /// phone's GPU, calibrated probabilities, one download of ~850 MB.
    case laya(id: String, directory: URL)
    /// Apple's on-device model (Foundation Models): nothing to download,
    /// seconds a question.
    case apple

    var title: String {
        switch self {
        case .laya: "Laya"
        case .apple: "Apple Intelligence"
        }
    }

    /// The name results carry as their backend (and the key of a rubric's
    /// `verdict.backends.<name>` cuts).
    var backendName: String {
        switch self {
        case .laya: "laya"
        case .apple: "foundation"
        }
    }
}

/// What the person picked in Settings → On this iPhone. Stored as a string:
/// "" (automatic), "apple", or "laya:<hub id>".
enum JudgePreference: Hashable, Sendable {
    case automatic
    case apple
    case laya(String)

    init(rawValue: String?) {
        switch rawValue ?? "" {
        case "apple": self = .apple
        case let raw where raw.hasPrefix("laya:") && raw.count > 5: self = .laya(String(raw.dropFirst(5)))
        default: self = .automatic
        }
    }

    var rawValue: String {
        switch self {
        case .automatic: ""
        case .apple: "apple"
        case .laya(let id): "laya:\(id)"
        }
    }
}

/// A Laya checkpoint on this device, fully downloaded.
struct LocalCheckpoint: Hashable, Sendable {
    var id: String
    var directory: URL
    /// Why it cannot run a check (too short a context, too few options); nil
    /// when it can.
    var problem: String?

    /// The checkpoint trained for typed decisions: the one a check prefers.
    var isTypedDecisions: Bool { id.contains("typed-decisions") }
}

/// Picks the judge a check on this iPhone uses, from what the person chose
/// and what the device has. An explicit choice wins while it is usable; after
/// that, in order: a downloaded typed-decisions checkpoint, Apple
/// Intelligence, any other downloaded checkpoint that can hold an idea. nil
/// means there is no on-device judge: Review then explains how to get one.
enum JudgeResolver {
    static func resolve(_ preference: JudgePreference, appleAvailable: Bool, downloaded: [LocalCheckpoint]) -> OnDeviceJudge? {
        let usable = downloaded.filter { $0.problem == nil }
        switch preference {
        case .laya(let id):
            if let c = usable.first(where: { $0.id == id }) { return .laya(id: c.id, directory: c.directory) }
        case .apple:
            if appleAvailable { return .apple }
        case .automatic:
            break
        }
        if let c = usable.first(where: \.isTypedDecisions) { return .laya(id: c.id, directory: c.directory) }
        if appleAvailable { return .apple }
        if let c = usable.first { return .laya(id: c.id, directory: c.directory) }
        return nil
    }
}

/// Who checks an idea when Settings has no stored choice: free users on the
/// phone, Pro users in Sparkjudge Cloud (what they pay for). A stored choice
/// always stands — a free user who picked Cloud spends the monthly free
/// hosted checks, and sees the paywall when they are used.
enum CheckRoute {
    static func resolve(stored: CheckerKind?, isPro: Bool) -> CheckerKind {
        stored ?? (isPro ? .remote : .onDevice)
    }
}

/// Whether a Laya checkpoint can run a check, from its manifest. The ANE
/// exports read 96 tokens and the "snake" export 64 with 4 options: an idea
/// and a question do not fit.
enum LayaFit {
    /// Tokens a check needs at least: the idea as a few sentences of JSON
    /// plus the longest question with its options. The standard exports read
    /// 512 or 1024.
    static let minimumTokens = 256

    /// The most answers any question of a check has: the scoring rubrics'
    /// options and levels, read from the core, and the router's one option
    /// per kind of idea (which `IdeaCategory` mirrors; the core lists
    /// scoring rubrics only). 10, a score's most levels, if they cannot be read.
    static let neededOptions: Int = {
        guard let json = try? sparkcoreRubrics(), let most = mostOptions(rubricsJSON: json) else { return 10 }
        return max(most, IdeaCategory.allCases.count)
    }()

    static func mostOptions(rubricsJSON: String) -> Int? {
        guard let rubrics = try? JSONSerialization.jsonObject(with: Data(rubricsJSON.utf8)) as? [String: Any] else { return nil }
        var most = 0
        for case let rubric as [String: Any] in rubrics.values {
            for case let q as [String: Any] in (rubric["questions"] as? [Any]) ?? [] {
                most = max(most, (q["options"] as? [String: Any])?.count ?? 0, (q["levels"] as? [Any])?.count ?? 0)
            }
        }
        return most > 0 ? most : nil
    }

    /// Why a checkpoint of this shape cannot run a check, in words; nil when it can.
    static func problem(maxLength: Int, maxOptions: Int, neededOptions: Int = neededOptions) -> String? {
        if maxLength < minimumTokens {
            return "Reads \(maxLength) tokens at a time: too few for an idea and a question."
        }
        if maxOptions < neededOptions {
            return "Takes \(maxOptions) answers per question; some questions have \(neededOptions)."
        }
        return nil
    }

    static func problem(_ manifest: CheckpointManifest) -> String? {
        // A fixed-shape graph reads its one length; the others read up to maxLength.
        problem(maxLength: manifest.maxLength, maxOptions: manifest.maxOptions)
    }
}

/// The engine settings a check on this iPhone runs with (`settings` in
/// mobile/sparkcore.go), as JSON for `SparkcoreEngine.make`.
struct EngineSettings: Encodable, Equatable, Sendable {
    var model: String
    var writerModel: String?
    var explain: Bool
    var extract: Bool
    /// An on-device model answers one request at a time: more only queues
    /// calls into their timeouts.
    var maxConcurrent = 1
    var questionSeconds: Double
    /// Each writer call, and also every question of one stage together
    /// (the core's Timeouts.Batch): it must outlast a whole scoring stage.
    var writerSeconds: Double

    enum CodingKeys: String, CodingKey {
        case model
        case writerModel = "writer_model"
        case explain, extract
        case maxConcurrent = "max_concurrent"
        case questionSeconds = "question_seconds"
        case writerSeconds = "writer_seconds"
    }

    static let appleModel = "apple-foundation-model"

    /// - Parameters:
    ///   - judge: who answers the questions.
    ///   - writer: whether Apple's model can write (extract and summary run
    ///     only then).
    static func make(judge: OnDeviceJudge, writer: Bool) -> EngineSettings {
        switch judge {
        case .laya(let id, _):
            // Milliseconds a question on a phone, about a second on the
            // simulator's CPU; the first question of a new length compiles a
            // graph (seconds). The stage bound covers the writer's calls.
            EngineSettings(model: id, writerModel: writer ? appleModel : nil, explain: writer, extract: writer,
                           questionSeconds: 60, writerSeconds: 180)
        case .apple:
            // 4–8 s a question alone, some up to 20 s; a stage's questions
            // go in one batch (FoundationJudge), ~10 s in the simulator, but
            // a batch that does not fit falls back to one at a time.
            EngineSettings(model: appleModel, writerModel: writer ? appleModel : nil, explain: writer, extract: writer,
                           questionSeconds: 60, writerSeconds: 600)
        }
    }

    var json: String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        return (try? encoder.encode(self)).map { String(decoding: $0, as: UTF8.self) } ?? "{}"
    }
}
