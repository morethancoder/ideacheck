// FoundationJudge and FoundationWriter put Apple's on-device model behind the
// Sparkcore protocols, so the whole check runs on the phone, free and offline.
//
// The core decides everything that is not a model call: which questions, with
// which prompts (configs/prompts, sent in each request), and what the answers
// add up to. This file only turns one request into one typed answer, using
// guided generation with a schema built at run time from the question's own
// options, so the model can only answer with a key the core knows.
//
// Every call arrives on a Go thread that waits for the answer, so each method
// blocks on its async work; never call them from the main thread. Each call
// gets its own LanguageModelSession: a session takes one request at a time.
// The on-device model answers one request at a time anyway, so run the engine
// with {"max_concurrent": 1}: more only queues calls into their timeouts.

import Foundation
import FoundationModels
import Sparkcore

/// What a Sparkcore judge request carries (see Judge in mobile/bridge.go).
struct SparkcoreRequest: Decodable {
    struct Option: Decodable {
        let key: String
        let description: String
    }
    struct Question: Decodable {
        let id: String
        let kind: String
        let instructions: String
        let options: [Option]?
        let levels: [String]?
    }
    let question: Question
    let system: String
    let prompt: String
}

enum SparkcoreSwiftError: LocalizedError {
    case badRequest(String)
    case unavailable(String)

    var errorDescription: String? {
        switch self {
        case .badRequest(let why): "bad request: \(why)"
        case .unavailable(let why): "the on-device model is unavailable: \(why)"
        }
    }
}

final class Outcome<T>: @unchecked Sendable {
    var result: Result<T, Error>?
}

/// Runs async work to completion on the calling (Go) thread.
func blockingCall<T: Sendable>(_ work: @escaping @Sendable () async throws -> T) throws -> T {
    let box = Outcome<T>()
    let done = DispatchSemaphore(value: 0)
    Task.detached {
        do { box.result = .success(try await work()) } catch { box.result = .failure(error) }
        done.signal()
    }
    done.wait()
    return try box.result!.get()
}

/// gomobile's protocols return a String and report failure through an
/// NSError pointer (they are not imported as `throws`); this adapts a
/// throwing body to that shape. The Go side sees the error's description.
public func sparkcoreCatching(_ error: NSErrorPointer, _ body: () throws -> String) -> String {
    do {
        return try body()
    } catch let failure {
        error?.pointee = NSError(
            domain: "sparkcore", code: 1,
            userInfo: [NSLocalizedDescriptionKey: failure.localizedDescription])
        return ""
    }
}

@available(iOS 26.0, macOS 26.0, *)
extension SystemLanguageModel.Availability {
    /// nil when the model can answer, else why not, in words for a person.
    var problem: String? {
        switch self {
        case .available: nil
        case .unavailable(.deviceNotEligible): "this device cannot run Apple Intelligence"
        case .unavailable(.appleIntelligenceNotEnabled): "Apple Intelligence is turned off in Settings"
        case .unavailable(.modelNotReady): "the model is still downloading"
        case .unavailable(let other): "\(other)"
        }
    }
}

/// Answers every typed question with SystemLanguageModel.default.
///
/// The answer is one greedy pick: `{"choice": key}`, `{"level": n}` or
/// `{"yes": bool}`. It carries no probabilities — the model exposes none — so
/// the core counts it as a single sure vote. Verdict cuts for this judge are
/// the rubrics' defaults until `verdict.backends.foundation` is tuned on bench.
@available(iOS 26.0, macOS 26.0, *)
public final class FoundationJudge: NSObject, SparkcoreJudgeProtocol, @unchecked Sendable {
    /// The name results carry as their backend, and the key a rubric's
    /// `verdict.backends.<name>` cuts would be found under.
    public static let backendName = "foundation"

    let model: SystemLanguageModel

    public init(model: SystemLanguageModel = .default) {
        self.model = model
    }

    public func name() -> String { Self.backendName }

    public func evaluate(_ requestJSON: String?, error: NSErrorPointer) -> String {
        sparkcoreCatching(error) {
            let request = try JSONDecoder().decode(SparkcoreRequest.self, from: Data((requestJSON ?? "").utf8))
            let model = self.model
            return try blockingCall { try await Self.answer(request, model: model) }
        }
    }

    static func answer(_ request: SparkcoreRequest, model: SystemLanguageModel) async throws -> String {
        if let problem = model.availability.problem {
            throw SparkcoreSwiftError.unavailable(problem)
        }
        let (field, schema) = try Self.schema(for: request.question)
        let session = LanguageModelSession(model: model, instructions: request.system)
        // The prompt already names the answer shape; the schema constrains the
        // decoding without being spelled out again (measured: 17 s → 6 s a
        // question in the simulator, with max_concurrent 1).
        let response = try await session.respond(
            to: request.prompt, schema: schema, includeSchemaInPrompt: false, options: GenerationOptions(sampling: .greedy))
        let reply: [String: Any]
        switch request.question.kind {
        case "choice":
            reply = ["choice": try response.content.value(String.self, forProperty: field)]
        case "score":
            let level = try response.content.value(String.self, forProperty: field)
            guard let n = Int(level) else { throw SparkcoreSwiftError.badRequest("level \(level)") }
            reply = ["level": n]
        default:
            reply = ["yes": try response.content.value(Bool.self, forProperty: field)]
        }
        let data = try JSONSerialization.data(withJSONObject: reply)
        return String(decoding: data, as: UTF8.self)
    }

    /// The one-property schema the answer must fit: the property name is the
    /// one the prompt's "Answer shape" line names, and its values are exactly
    /// the question's option keys or level indices.
    static func schema(for q: SparkcoreRequest.Question) throws -> (String, GenerationSchema) {
        let field: String
        let value: DynamicGenerationSchema
        switch q.kind {
        case "choice":
            let keys = (q.options ?? []).map(\.key)
            guard !keys.isEmpty else { throw SparkcoreSwiftError.badRequest("\(q.id) has no options") }
            field = "choice"
            value = DynamicGenerationSchema(name: "\(q.id)_choice", anyOf: keys)
        case "score":
            let levels = q.levels ?? []
            guard levels.count >= 2 else { throw SparkcoreSwiftError.badRequest("\(q.id) has no levels") }
            field = "level"
            value = DynamicGenerationSchema(name: "\(q.id)_level", anyOf: levels.indices.map(String.init))
        case "noul":
            field = "yes"
            value = DynamicGenerationSchema(type: Bool.self)
        default:
            throw SparkcoreSwiftError.badRequest("unknown kind \(q.kind)")
        }
        let root = DynamicGenerationSchema(
            name: "Answer", properties: [.init(name: field, schema: value)])
        return (field, try GenerationSchema(root: root, dependencies: []))
    }
}

/// The writer's two jobs on the same on-device model: read the stated facts
/// out of the idea (extract) and write the paragraph on why (narrate). The
/// prompts come from the core, as for the judge.
@available(iOS 26.0, macOS 26.0, *)
public final class FoundationWriter: NSObject, SparkcoreWriterProtocol, @unchecked Sendable {
    let model: SystemLanguageModel

    public init(model: SystemLanguageModel = .default) {
        self.model = model
    }

    public func name() -> String { FoundationJudge.backendName }

    /// fieldsJSON is ["problem", …]; the answer is {"problem": "…", …} with
    /// only the fields the document states. Each field is optional in the
    /// schema, so "not stated" is an answer the model can give.
    public func extract(_ system: String?, user: String?, fieldsJSON: String?, error: NSErrorPointer) -> String {
        sparkcoreCatching(error) { try extract(system ?? "", user ?? "", fieldsJSON ?? "[]") }
    }

    func extract(_ system: String, _ user: String, _ fieldsJSON: String) throws -> String {
        let fields = try JSONDecoder().decode([String].self, from: Data(fieldsJSON.utf8))
        let model = self.model
        return try blockingCall {
            if let problem = model.availability.problem { throw SparkcoreSwiftError.unavailable(problem) }
            let root = DynamicGenerationSchema(
                name: "Stated",
                properties: fields.map { .init(name: $0, schema: DynamicGenerationSchema(type: String.self), isOptional: true) })
            let schema = try GenerationSchema(root: root, dependencies: [])
            let session = LanguageModelSession(model: model, instructions: system)
            let response = try await session.respond(to: user, schema: schema, options: GenerationOptions(sampling: .greedy))
            var values: [String: String] = [:]
            for field in fields {
                if let v = try response.content.value(String?.self, forProperty: field),
                    !v.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                {
                    values[field] = v
                }
            }
            let data = try JSONSerialization.data(withJSONObject: values)
            return String(decoding: data, as: UTF8.self)
        }
    }

    public func narrate(_ system: String?, brief: String?, error: NSErrorPointer) -> String {
        let (model, system, brief) = (self.model, system ?? "", brief ?? "")
        return sparkcoreCatching(error) {
            try blockingCall {
                if let problem = model.availability.problem { throw SparkcoreSwiftError.unavailable(problem) }
                // explain.tmpl asks for {"summary": "<the paragraph>"}.
                let root = DynamicGenerationSchema(
                    name: "Summary", properties: [.init(name: "summary", schema: DynamicGenerationSchema(type: String.self))])
                let schema = try GenerationSchema(root: root, dependencies: [])
                let session = LanguageModelSession(model: model, instructions: system)
                let response = try await session.respond(to: brief, schema: schema)
                return try response.content.value(String.self, forProperty: "summary")
            }
        }
    }
}
