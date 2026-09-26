// LayaDeviceJudge puts a Laya typed-decision checkpoint (LayaKit, Core ML)
// behind the Sparkcore judge protocol: the free offline judge that answers in
// milliseconds a question instead of seconds.
//
// Laya reads the question and the state, not a rendered prompt: like the
// desktop laya backend (internal/judge/laya, Jev's wire format), it turns the
// request's question into Laya's {type, instructions, criteria} and reads
// `state` as the JSON the core sent, so the same check asks the model the
// same thing on the desk and on the phone. The request's system and prompt
// are for generative judges and go unused here.
//
// The answer carries the model's calibrated probabilities, not one pick: a
// choice with every option's probability, a score as the expected level, a
// noul as p(true) — the shape the desktop backend reports.

import Foundation
import LayaKit
import Sparkcore
import os

@available(iOS 26.0, macOS 26.0, *)
public final class LayaDeviceJudge: NSObject, SparkcoreJudgeProtocol, @unchecked Sendable {
    /// The name results carry as their backend, and the key a rubric's
    /// `verdict.backends.<name>` cuts are found under: the desktop's, since
    /// it is the same model answering the same questions.
    public static let backendName = "laya"

    let directory: URL
    let log = Logger(subsystem: "com.morethancoder.sparkcore", category: "laya")

    /// - Parameter directory: a downloaded checkpoint
    ///   (`LayaDownloader.directory(for:)`). It is loaded on the first
    ///   question — seconds, the first time a checkpoint is compiled — and
    ///   kept for every judge made over the same directory.
    public init(directory: URL) {
        self.directory = directory
    }

    public func name() -> String { Self.backendName }

    public func evaluate(_ requestJSON: String?, error: NSErrorPointer) -> String {
        sparkcoreCatching(error) { try self.evaluate(requestJSON ?? "") }
    }

    func evaluate(_ requestJSON: String) throws -> String {
        let request = try JSONValue(parsing: requestJSON)
        guard let q = request["question"] else { throw SparkcoreSwiftError.badRequest("no question") }
        let question = try Self.question(q)
        let model = try Self.model(at: directory)
        let answer = try LayaJudge(model: model).evaluate(state: .json(request["state"] ?? .object([])), question: question)
        if answer.droppedStateTokens > 0 {
            // The desktop backend warns the same way: without it a check
            // silently scores a shortened idea.
            log.warning("laya: \(q["id"]?.stringValue ?? "?", privacy: .public) read a state cut to fit: \(answer.droppedStateTokens) of its tokens did not fit \(model.maxLength)")
        }
        return Self.reply(answer, model: model.manifest.repository ?? directory.lastPathComponent)
    }

    /// The request's question in Laya's schema: choice options in the order
    /// sent (the core sorts them by key, as Go marshals the desktop's map),
    /// score levels lowest first, noul criteria as {"true", "false"}.
    static func question(_ q: JSONValue) throws -> LayaQuestion {
        let instructions = q["instructions"]?.stringValue ?? ""
        switch q["kind"]?.stringValue {
        case "choice":
            let options = (q["options"]?.arrayValue ?? []).compactMap { o -> (String, String)? in
                guard let key = o["key"]?.stringValue else { return nil }
                return (key, o["description"]?.stringValue ?? "")
            }
            guard !options.isEmpty else { throw SparkcoreSwiftError.badRequest("\(q["id"]?.stringValue ?? "?") has no options") }
            return .choice(instructions, options: options)
        case "score":
            let levels = (q["levels"]?.arrayValue ?? []).compactMap(\.stringValue)
            guard levels.count >= 2 else { throw SparkcoreSwiftError.badRequest("\(q["id"]?.stringValue ?? "?") has no levels") }
            return .score(instructions, levels: levels)
        case "noul":
            let c = q["criteria"]
            return .noul(instructions, yes: c?["yes"]?.stringValue, no: c?["no"]?.stringValue)
        case let other:
            throw SparkcoreSwiftError.badRequest("unknown kind \(other ?? "none")")
        }
    }

    /// {"choice"|"level"|"probability", "probabilities", "confidence",
    /// "model", "method", "tokens_in"} — see Judge in mobile/bridge.go. A
    /// noul sends neither probabilities nor confidence: the core derives its
    /// confidence from p, as it does for the desktop backend.
    static func reply(_ a: LayaAnswer, model: String) -> String {
        var out: [String: Any] = ["model": model, "method": backendName, "tokens_in": a.tokens]
        let probs = Dictionary(a.probabilities.map { ($0.label, $0.p) }, uniquingKeysWith: { x, _ in x })
        switch a.kind {
        case .choice:
            out["choice"] = a.choice ?? ""
            out["probabilities"] = probs
            out["confidence"] = a.confidence
        case .score:
            out["level"] = a.score ?? 0
            out["probabilities"] = probs
            out["confidence"] = a.confidence
        case .noul:
            out["probability"] = a.noul ?? 0
        }
        if a.droppedStateTokens > 0 { out["state_tokens_dropped"] = a.droppedStateTokens }
        let data = (try? JSONSerialization.data(withJSONObject: out, options: [.sortedKeys])) ?? Data("{}".utf8)
        return String(decoding: data, as: UTF8.self)
    }

    // One loaded model per checkpoint directory, shared by every judge (an
    // engine is built per check; a model is hundreds of megabytes).
    nonisolated(unsafe) static var loaded: [URL: LayaModel] = [:]
    static let loading = NSLock()

    static func model(at directory: URL) throws -> LayaModel {
        loading.lock()
        defer { loading.unlock() }
        if let m = loaded[directory] { return m }
        let m = try LayaModel(directory: directory)
        loaded[directory] = m
        return m
    }

    /// Loads (and on first use compiles) the checkpoint ahead of the first
    /// question, off the main thread, so the first check does not wait on it.
    public static func preload(_ directory: URL) async throws {
        try await Task.detached(priority: .utility) { try preloadBlocking(directory) }.value
    }

    /// preload on the calling thread (never the main one).
    public static func preloadBlocking(_ directory: URL) throws {
        try model(at: directory).prepare()
    }

    /// Drops loaded models (memory pressure, or a checkpoint deleted).
    public static func unload() {
        loading.lock()
        loaded.removeAll()
        loading.unlock()
    }
}
