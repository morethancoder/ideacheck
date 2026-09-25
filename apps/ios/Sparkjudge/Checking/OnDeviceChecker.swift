import Foundation
import FoundationModels
import LayaKit
import Sparkcore
import os

/// Checks an idea on this iPhone, free and offline: the Go core
/// (Sparkcore.xcframework, `make mobile`) runs the whole check — rubrics,
/// prompts, arithmetic and verdict from its embedded configs — and Swift
/// brings the models. The judge is Laya on Core ML or Apple's on-device model
/// (`OnDeviceJudge`); the writer (reading stated facts, the summary) is
/// Apple's model when it is available, else there is none and those two
/// steps are skipped. Nothing is searched: questions that need the web are
/// skipped, as in any check without research.
///
/// The core's events arrive as `CheckEvent.progress`, its result JSON as
/// `CheckEvent.result`, so nothing above this file knows where a check ran.
struct OnDeviceChecker: IdeaChecker {
    var kind: CheckerKind { .onDevice }

    /// nil when this iPhone has no judge: a check then says how to get one.
    let judge: OnDeviceJudge?
    /// Whether Apple's model writes (extract and summary).
    let writer: Bool
    /// Tests: this judge in place of the one `judge` names.
    var swiftJudge: (any SparkcoreJudgeProtocol & Sendable)?

    init(judge: OnDeviceJudge?, writer: Bool, swiftJudge: (any SparkcoreJudgeProtocol & Sendable)? = nil) {
        self.judge = judge
        self.writer = writer
        self.swiftJudge = swiftJudge
    }

    static let log = Logger(subsystem: "com.morethancoder.sparkjudge", category: "on-device")

    func check(_ intake: Intake, rubric: String?) -> AsyncThrowingStream<CheckEvent, any Error> {
        AsyncThrowingStream { continuation in
            guard let judge else {
                continuation.finish(throwing: OnDeviceError.noJudge)
                return
            }
            let run = OnDeviceRun(judge: judge, writer: writer, intake: intake, rubric: rubric, continuation: continuation)
            run.swiftJudge = swiftJudge
            continuation.onTermination = { _ in run.cancel() }
            // A check blocks its thread for its whole length (the core calls
            // back into Swift and waits): a thread of its own, not one of the
            // cooperative pool's few.
            let thread = Thread { run.main() }
            thread.name = "sparkcore check"
            thread.qualityOfService = .userInitiated
            thread.stackSize = 2 << 20
            thread.start()
        }
    }
}

/// One check on its own thread; `cancel` may come from any other.
final class OnDeviceRun: @unchecked Sendable {
    let judge: OnDeviceJudge
    let writer: Bool
    let intake: Intake
    let rubric: String?
    let continuation: AsyncThrowingStream<CheckEvent, any Error>.Continuation

    var swiftJudge: (any SparkcoreJudgeProtocol)?

    private let lock = NSLock()
    private var check: SparkcoreCheck?
    private var cancelled = false

    init(judge: OnDeviceJudge, writer: Bool, intake: Intake, rubric: String?,
         continuation: AsyncThrowingStream<CheckEvent, any Error>.Continuation) {
        self.judge = judge
        self.writer = writer
        self.intake = intake
        self.rubric = rubric
        self.continuation = continuation
    }

    func cancel() {
        let check = lock.withLock {
            cancelled = true
            return self.check
        }
        check?.cancel()
    }

    private var isCancelled: Bool { lock.withLock { cancelled } }

    func main() {
        let start = Date()
        do {
            let result = try run()
            OnDeviceChecker.log.info("on-device check with \(self.judge.title, privacy: .public): \(result.verdict ?? "no verdict", privacy: .public) in \(String(format: "%.1f", Date().timeIntervalSince(start)), privacy: .public) s (\(self.relay.stages, privacy: .public))")
            continuation.finish()
        } catch {
            let friendly = OnDeviceError.friendly(error, judge: judge)
            if !(friendly is CancellationError) {
                OnDeviceChecker.log.error("on-device check failed: \(String(describing: error), privacy: .public)")
            }
            continuation.finish(throwing: friendly)
        }
    }

    private lazy var relay = EventRelay(
        plan: ScorePlan(rubricsJSON: Self.rubricsJSON, rubric: rubric, hasProfile: !(intake.profile ?? [:]).isEmpty)
    ) { [continuation] p in continuation.yield(.progress(p)) }

    private func run() throws -> CheckResult {
        let judgeObject = try makeJudge()
        if isCancelled { throw CancellationError() }
        let settings = EngineSettings.make(judge: judge, writer: writer)
        let writerObject: (any SparkcoreWriterProtocol)? = writer ? FoundationWriter() : nil
        let engine: SparkcoreEngine =
            if let batch = judgeObject as? any SparkcoreBatchJudgeProtocol {
                try SparkcoreEngine.make(settingsJSON: settings.json, batchJudge: batch, writer: writerObject)
            } else {
                try SparkcoreEngine.make(settingsJSON: settings.json, judge: judgeObject, writer: writerObject)
            }
        let intakeJSON = String(decoding: try JSONEncoder().encode(intake), as: UTF8.self)
        let prepared = try engine.prepare(intakeJSON, optionsJSON: Self.options(rubric: rubric))
        let cancelledEarly = lock.withLock {
            check = prepared
            return cancelled
        }
        if cancelledEarly { throw CancellationError() }

        let json = try prepared.run(relay)
        let data = Data(json.utf8)
        let result = try CheckResult.decode(data)
        if result.status == "error" {
            throw OnDeviceError.failed(result)
        }
        continuation.yield(.result(result, raw: data))
        return result
    }

    /// The Swift judge. Laya's checkpoint is loaded (and, the first time,
    /// compiled) here, before the check, so no question waits on it.
    private func makeJudge() throws -> any SparkcoreJudgeProtocol {
        if let swiftJudge { return swiftJudge }
        switch judge {
        case .laya(_, let directory):
            guard FileManager.default.fileExists(atPath: directory.appendingPathComponent("coreml_config.json").path) else {
                throw OnDeviceError.layaMissing
            }
            let load = CheckQuestion(id: "judge", kind: nil, instructions: "Load Laya")
            relay.relay(CheckProgress(type: "started", stage: "load", question: load))
            do {
                try LayaDeviceJudge.preloadBlocking(directory)
            } catch {
                throw OnDeviceError.layaFailed(error.localizedDescription)
            }
            relay.relay(CheckProgress(type: "answered", stage: "load", question: load))
            return LayaDeviceJudge(directory: directory)
        case .apple:
            if let problem = SystemLanguageModel.default.availability.problem {
                throw OnDeviceError.appleUnavailable(problem)
            }
            return FoundationJudge()
        }
    }

    /// {"rubric": …} when the person chose a kind of idea; "" lets the router pick.
    static func options(rubric: String?) -> String {
        guard let rubric, !rubric.isEmpty,
            let data = try? JSONSerialization.data(withJSONObject: ["rubric": rubric])
        else { return "" }
        return String(decoding: data, as: UTF8.self)
    }

    /// The core's rubrics, read once: what the scoring stage will ask.
    static let rubricsJSON: String = (try? sparkcoreRubrics()) ?? "{}"
}

/// Hands the core's events to the check's stream, one at a time, with how
/// many questions the scoring stage asks once that is known.
final class EventRelay: NSObject, SparkcoreListenerProtocol, @unchecked Sendable {
    private var plan: ScorePlan
    private let send: @Sendable (CheckProgress) -> Void
    private let lock = NSLock()
    private let start = Date()
    /// Each stage's first and last event, in seconds since the check began.
    private var spans: [(stage: String, from: TimeInterval, to: TimeInterval)] = []

    /// Where the check spent its time: "load 1.9s, extract 9.8s, …".
    var stages: String {
        lock.withLock { spans.map { "\($0.stage) \(String(format: "%.1f", $0.to - $0.from))s" }.joined(separator: ", ") }
    }

    init(plan: ScorePlan, send: @escaping @Sendable (CheckProgress) -> Void) {
        self.plan = plan
        self.send = send
    }

    func onEvent(_ eventJSON: String?) {
        guard let json = eventJSON, let p = SparkcoreEvent.progress(json) else { return }
        relay(p)
    }

    /// One step to the stream: the core's, or the app's own (loading Laya).
    func relay(_ event: CheckProgress) {
        var p = event
        lock.withLock {
            let expected = plan.see(p)
            if p.stage == "score" { p.expected = expected }
            let t = Date().timeIntervalSince(start)
            if spans.last?.stage == p.stage { spans[spans.count - 1].to = t } else { spans.append((p.stage, t, t)) }
        }
        send(p)
    }
}

/// Why a check on this iPhone did not end with a score, in words for a person.
enum OnDeviceError: LocalizedError, Equatable {
    case noJudge
    case appleUnavailable(String)
    case layaMissing
    case layaFailed(String)
    case noScore(String)
    case engine(String)

    /// Why a result has no score: the most common reason among the
    /// questions that failed.
    static func failed(_ result: CheckResult) -> OnDeviceError {
        let reasons = result.answers.compactMap(\.error).filter { !$0.hasPrefix("no ") }
        let counts = Dictionary(reasons.map { ($0, 1) }, uniquingKeysWith: +)
        guard let top = counts.max(by: { $0.value < $1.value })?.key else {
            return .noScore(result.error ?? "no question was answered")
        }
        if let range = top.range(of: "the on-device model is unavailable: ") {
            return .appleUnavailable(String(top[range.upperBound...]))
        }
        return .noScore(top)
    }

    var errorDescription: String? {
        switch self {
        case .noJudge:
            "There's no judge on this iPhone yet. Download Laya in Settings → Judge, or check with Sparkjudge Cloud."
        case .appleUnavailable(let why):
            "Apple Intelligence can't check right now: \(why). Download Laya in Settings → Judge to check without it."
        case .layaMissing:
            "The Laya model isn't on this iPhone anymore. Download it again in Settings → Judge."
        case .layaFailed(let why):
            "Laya couldn't start on this iPhone (\(why)). Remove it and download it again in Settings → Judge."
        case .noScore(let why):
            "The judge couldn't score this idea: \(OnDeviceError.shorten(why))"
        case .engine(let why):
            "The check stopped on this iPhone: \(OnDeviceError.shorten(why))"
        }
    }

    /// The error a check's stream ends with: cancellations as
    /// CancellationError, the core's own errors in plain words.
    static func friendly(_ error: any Error, judge: OnDeviceJudge) -> any Error {
        if error is CancellationError || error is OnDeviceError { return error }
        let text = (error as NSError).localizedDescription
        if text.contains("check cancelled") { return CancellationError() }
        if let laya = error as? LayaError { return OnDeviceError.layaFailed(laya.localizedDescription) }
        return OnDeviceError.engine(text)
    }

    /// The first line, without the core's package prefix, at most 160 characters.
    static func shorten(_ text: String) -> String {
        var line = text.split(separator: "\n", maxSplits: 1).first.map(String.init) ?? text
        if line.hasPrefix("sparkcore: ") { line.removeFirst("sparkcore: ".count) }
        if line.count > 160 { line = String(line.prefix(157)) + "…" }
        return line
    }
}
