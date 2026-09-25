import Foundation
import SwiftData

enum IdeaStatus: String, Codable, Sendable {
    /// Saved the moment a take ended; not reviewed yet.
    case draft
    /// Reviewed, never checked.
    case reviewed
    case checking
    case checked
    case failed
}

/// One captured idea. CloudKit-compatible: every stored property has a default
/// or is optional, and nothing is marked unique.
@Model
final class Idea {
    var id: UUID = UUID()
    var createdAt: Date = Date.now
    var updatedAt: Date = Date.now
    var transcript: String = ""
    var title: String = ""
    var categoryRaw: String = IdeaCategory.other.rawValue
    /// True once the person chose the category themselves; a check then keeps it.
    var categoryChosen: Bool = false
    /// Idea fields keyed by `FieldSpec.name`, JSON-encoded.
    var fieldsData: Data? = nil
    var statusRaw: String = IdeaStatus.draft.rawValue
    /// Whether the on-device writer has already made its suggestions.
    var suggested: Bool = false

    /// The full check result, JSON exactly as the engine returned it.
    var resultData: Data? = nil
    /// Denormalized from the result so lists never decode JSON per card.
    var composite: Double? = nil
    var verdictRaw: String? = nil
    var checkedAt: Date? = nil
    var lastError: String? = nil

    /// Seeds the card's generated background. Derived from `id` at creation.
    var styleSeed: Int64 = 0

    /// `IdeaOrigin` as JSON: mixed from other ideas, or started from a trend.
    var originData: Data? = nil

    init(transcript: String = "", title: String = "", category: IdeaCategory = .other, createdAt: Date = .now) {
        let id = UUID()
        self.id = id
        self.transcript = transcript
        self.title = title
        self.categoryRaw = category.rawValue
        self.createdAt = createdAt
        self.updatedAt = createdAt
        self.styleSeed = Int64(bitPattern: CardStyle.seed(for: id))
    }

    var category: IdeaCategory {
        get { IdeaCategory(routerChoice: categoryRaw) }
        set { categoryRaw = newValue.rawValue }
    }

    var status: IdeaStatus {
        get { IdeaStatus(rawValue: statusRaw) ?? .draft }
        set { statusRaw = newValue.rawValue }
    }

    var verdict: Verdict? { verdictRaw.flatMap(Verdict.init(rawValue:)) }

    var fields: [String: String] {
        get { fieldsData.flatMap { try? JSONDecoder().decode([String: String].self, from: $0) } ?? [:] }
        set { fieldsData = try? JSONEncoder().encode(newValue.filter { !$0.value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }) }
    }

    var result: CheckResult? {
        resultData.flatMap { try? CheckResult.decode($0) }
    }

    var displayTitle: String {
        let t = title.trimmingCharacters(in: .whitespacesAndNewlines)
        if !t.isEmpty { return t }
        let words = transcript.split(separator: " ").prefix(6).joined(separator: " ")
        return words.isEmpty ? "Untitled spark" : words + (transcript.split(separator: " ").count > 6 ? "…" : "")
    }

    var seed: UInt64 { UInt64(bitPattern: styleSeed) }

    /// What a checker receives. The transcript is the idea; the title rides as a field.
    var intake: Intake {
        var f = fields
        let t = title.trimmingCharacters(in: .whitespacesAndNewlines)
        if !t.isEmpty { f["title"] = t }
        let idea = transcript.trimmingCharacters(in: .whitespacesAndNewlines)
        return Intake(idea: idea.isEmpty ? t : idea, fields: f.isEmpty ? nil : f)
    }

    /// Stores a finished result and the values the cards show.
    func apply(_ result: CheckResult, raw: Data) {
        resultData = raw
        composite = result.composite
        verdictRaw = result.verdict
        checkedAt = .now
        updatedAt = .now
        lastError = nil
        status = result.status == "error" ? .failed : .checked
        if !categoryChosen, let choice = result.ideaType?.choice {
            category = IdeaCategory(routerChoice: choice)
        }
    }
}
