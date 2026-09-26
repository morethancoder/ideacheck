import Foundation
import FoundationModels

/// What the writer suggests for a fresh take. Empty strings mean "not said".
struct IdeaSuggestion: Sendable, Equatable {
    var title: String
    var category: IdeaCategory
    var fields: [String: String]
}

enum WriterAvailability: Sendable, Equatable {
    case available
    case unavailable(String)

    var isAvailable: Bool { self == .available }
}

/// Reads a transcript and suggests a title, category and idea fields.
protocol IdeaSuggester: Sendable {
    var availability: WriterAvailability { get }
    func suggest(transcript: String) async throws -> IdeaSuggestion
}

/// The notes Apple's on-device model fills in. Field names follow
/// `configs/fields.yaml`; the long descriptions ride in the instructions, taken
/// from the bundled catalogue, so the model reads the same meaning the engine does.
@Generable(description: "Notes about an idea someone just dictated")
struct IdeaNotes {
    @Guide(description: "A short, memorable name for the idea: two to five words, no quotes, no trailing period")
    var title: String

    @Guide(description: "What kind of idea this is", .anyOf(["business", "side_project", "content", "research", "creative", "other"]))
    var category: String

    @Guide(description: "The specific problem it solves and for whom. Empty if not said.")
    var problem: String

    @Guide(description: "The first users, narrowly. Empty if not said.")
    var audience: String

    @Guide(description: "What would actually be built or made. Empty if not said.")
    var solution: String

    @Guide(description: "What changed recently that makes this possible or needed now. Empty if not said.")
    var whyNow: String

    @Guide(description: "How it would make money, or that it is not meant to. Empty if not said.")
    var monetization: String

    @Guide(description: "What people use today instead. Empty if not said.")
    var competitorsKnown: String

    @Guide(description: "The one thing this does that existing options do not. Empty if not said.")
    var differentiation: String

    var fields: [String: String] {
        [
            "problem": problem, "audience": audience, "solution": solution, "why_now": whyNow,
            "monetization": monetization, "competitors_known": competitorsKnown, "differentiation": differentiation,
        ].filter { !$0.value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
    }
}

/// The writer role on the phone: Apple's on-device foundation model. When the
/// model is unavailable (device not eligible, Apple Intelligence off, model
/// still downloading) the review screen simply shows no suggestions.
struct OnDeviceWriter: IdeaSuggester {
    var availability: WriterAvailability {
        let model = SystemLanguageModel.default
        switch model.availability {
        case .available:
            return model.supportsLocale() ? .available : .unavailable("Apple Intelligence doesn't support this language yet.")
        case .unavailable(.deviceNotEligible):
            return .unavailable("This device can't run Apple Intelligence, so fill in the details yourself.")
        case .unavailable(.appleIntelligenceNotEnabled):
            return .unavailable("Turn on Apple Intelligence in Settings to get suggestions here.")
        case .unavailable(.modelNotReady):
            return .unavailable("Apple Intelligence is still getting ready. Suggestions will appear once it has downloaded.")
        case .unavailable:
            return .unavailable("Apple Intelligence isn't available right now.")
        }
    }

    func suggest(transcript: String) async throws -> IdeaSuggestion {
        let session = LanguageModelSession(instructions: Self.instructions())
        let response = try await session.respond(
            to: "Transcript:\n\(transcript)",
            generating: IdeaNotes.self,
            options: GenerationOptions(temperature: 0.2)
        )
        let notes = response.content
        return IdeaSuggestion(
            title: notes.title.trimmingCharacters(in: .whitespacesAndNewlines.union(CharacterSet(charactersIn: "\"“”."))),
            category: IdeaCategory(routerChoice: notes.category),
            fields: notes.fields
        )
    }

    /// `Resources/writer_instructions.md` plus each field's meaning from `fields.json`.
    static func instructions(bundle: Bundle = .main) -> String {
        let base = bundle.url(forResource: "writer_instructions", withExtension: "md")
            .flatMap { try? String(contentsOf: $0, encoding: .utf8) }
            ?? "Fill in notes about the dictated idea. Use only what the transcript says; leave a field empty when it is not said."
        let fields = FieldCatalog.shared.idea.map { "- \($0.name): \($0.description)" }.joined(separator: "\n")
        return base + "\n\nWhat each field means:\n" + fields
    }
}

/// A writer that is never available — for previews, tests and demo runs.
struct NoWriter: IdeaSuggester {
    var reason = "Suggestions are off."
    var availability: WriterAvailability { .unavailable(reason) }
    func suggest(transcript: String) async throws -> IdeaSuggestion {
        throw CheckError.unavailable(reason)
    }
}
