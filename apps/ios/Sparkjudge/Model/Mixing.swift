import Foundation

/// One idea going into a mix: what the writer reads of it.
struct MixInput: Sendable, Hashable, Identifiable {
    var id: UUID
    var title: String
    /// The idea in the person's words (the transcript).
    var idea: String
    var fields: [String: String]

    init(id: UUID = UUID(), title: String, idea: String, fields: [String: String] = [:]) {
        self.id = id
        self.title = title
        self.idea = idea
        self.fields = fields
    }

    @MainActor init(_ idea: Idea) {
        self.init(id: idea.id, title: idea.displayTitle,
                  idea: idea.transcript.trimmingCharacters(in: .whitespacesAndNewlines),
                  fields: idea.fields.filter { $0.key != "title" })
    }
}

/// The new idea a writer proposes. Field names follow `configs/fields.yaml`.
struct MixedIdea: Sendable, Equatable {
    var title: String
    var problem: String
    var audience: String
    var solution: String

    /// The idea fields to store: the non-empty ones, title aside.
    var fields: [String: String] {
        ["problem": problem, "audience": audience, "solution": solution]
            .mapValues { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.value.isEmpty }
    }

    /// The idea in a sentence or two, for "What you said": what it is, then why.
    var pitch: String {
        [solution, problem].map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
            .map { $0.hasSuffix(".") ? $0 : $0 + "." }
            .joined(separator: " ")
    }

    /// A new draft to review, linked to the ideas it came from.
    @MainActor func draft(from inputs: [MixInput], writer: String) -> Idea {
        let idea = Idea(transcript: pitch, title: title.trimmingCharacters(in: .whitespacesAndNewlines.union(CharacterSet(charactersIn: "\"“”."))))
        idea.fields = fields
        idea.suggested = true // the writer already filled it in
        idea.origin = .mixed(from: inputs, writer: writer)
        return idea
    }
}

/// `POST /v1/mix` on the hosted API (internal/sparkjudge/lab.go).
struct MixRequest: Codable, Sendable, Equatable {
    struct Item: Codable, Sendable, Equatable {
        var title: String?
        var idea: String
        var fields: [String: String]?
    }

    var ideas: [Item]

    init(_ inputs: [MixInput]) {
        ideas = inputs.map { input in
            let fields = input.fields.filter { !$0.value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
            return Item(title: input.title.isEmpty ? nil : input.title, idea: input.idea, fields: fields.isEmpty ? nil : fields)
        }
    }
}

struct MixResponse: Codable, Sendable, Equatable {
    var fields: [String: String]
    var model: String?

    enum CodingKeys: String, CodingKey {
        case fields, model
    }

    var mixed: MixedIdea {
        MixedIdea(title: fields["title"] ?? "", problem: fields["problem"] ?? "",
                  audience: fields["audience"] ?? "", solution: fields["solution"] ?? "")
    }
}
