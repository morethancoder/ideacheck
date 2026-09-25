import Foundation

/// What a checker receives: the same shape as the engine's intake JSON
/// (`pipeline.Intake`), so it can be POSTed as is.
struct Intake: Codable, Sendable, Hashable {
    var idea: String
    var context: String?
    var fields: [String: String]?
    var profile: [String: String]?
}

/// A question the engine is asking, as it appears in a progress event.
struct CheckQuestion: Codable, Sendable, Hashable {
    var id: String
    var kind: String?
    var instructions: String?
    var weight: Double?
    var polarity: Int?
}

/// One progress event from the engine (`pipeline.Event`): a question started,
/// was answered or failed, in the preflight (gaps + router) or score stage.
struct CheckProgress: Codable, Sendable, Hashable {
    var type: String
    var stage: String
    var question: CheckQuestion
    var answer: CheckResult.Answer?
    var value: Double?

    var isFinished: Bool { type == "answered" || type == "failed" }
}

enum CheckEvent: Sendable {
    /// The check was accepted and has an id.
    case accepted(id: String)
    case progress(CheckProgress)
    /// Always the last event of a successful check. `raw` is the JSON as received.
    case result(CheckResult, raw: Data)
}

enum CheckerKind: String, CaseIterable, Codable, Sendable, Identifiable {
    case remote
    case preview
    case onDevice = "on_device"

    var id: String { rawValue }

    var title: String {
        switch self {
        case .remote: "ideacheck server"
        case .preview: "Offline preview"
        case .onDevice: "On this iPhone"
        }
    }

    var detail: String {
        switch self {
        case .remote: "Your ideacheck server scores the idea with the judge it is configured with."
        case .preview: "A realistic sample result, made on the phone. Nothing is judged; for trying the app."
        case .onDevice: "The ideacheck engine with Apple's on-device model as judge. Coming in a later version."
        }
    }
}

enum CheckError: LocalizedError, Equatable {
    case unavailable(String)
    case badResponse(status: Int, message: String)
    case server(String)
    case streamEnded

    var errorDescription: String? {
        switch self {
        case .unavailable(let why): why
        case .badResponse(let status, let message): "The server answered \(status): \(message)"
        case .server(let message): message
        case .streamEnded: "The check stopped before it sent a result."
        }
    }
}

/// Anything that can check an idea. A check is a stream of progress events
/// that ends with exactly one `.result`, or throws.
protocol IdeaChecker: Sendable {
    var kind: CheckerKind { get }
    /// `rubric` names a rubric to score with; nil lets the engine's router choose.
    func check(_ intake: Intake, rubric: String?) -> AsyncThrowingStream<CheckEvent, any Error>
}
