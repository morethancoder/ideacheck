import Foundation
import SwiftData

/// Sample ideas for previews, screenshots and the `-sjSeed YES` launch argument.
/// Checked ones carry `PreviewChecker` results, except the dentist to-do app,
/// which carries the real engine result the tests decode.
@MainActor
enum SampleData {
    struct Sample {
        var title: String
        var category: IdeaCategory
        var transcript: String
        var fields: [String: String]
        var daysAgo: Double
        var checked = true
    }

    static let samples: [Sample] = [
        Sample(title: "Vector Thread", category: .business,
               transcript: "A gallery of design system files rendered live, so designers can hand one to a coding agent and actually get their system back.",
               fields: ["problem": "designers hand a design system to an AI coding agent and get back code that ignores it",
                        "audience": "designers who already keep a DESIGN.md and work with coding agents daily",
                        "why_now": "coding agents now read design files directly"], daysAgo: 1),
        Sample(title: "Walk Letters", category: .sideProject,
               transcript: ScriptedTranscriber.defaultScript,
               fields: ["solution": "record on walks, get a Sunday letter with the ideas sorted by effort"], daysAgo: 2),
        Sample(title: "The Lighthouse Tapes", category: .creative,
               transcript: "An audio drama told only through the cassette tapes a lighthouse keeper mails to his daughter, one per episode.",
               fields: ["audience": "people who loved Welcome to Night Vale and The Magnus Archives"], daysAgo: 3),
        Sample(title: "Tiny Theories", category: .content,
               transcript: "A weekly newsletter with one small, testable theory about how creative work happens, and a way to try it this week.",
               fields: ["audience": "illustrators and writers who read about process more than they'd admit"], daysAgo: 4),
        Sample(title: "Cartographer's Deck", category: .creative,
               transcript: "A card game where every card is a piece of a map and you win by making the map tell a story.",
               fields: [:], daysAgo: 6),
        Sample(title: "Deadline Drift", category: .research,
               transcript: "A small study: do self-set deadlines make ideas better or just make them finished? Log fifty projects for three months.",
               fields: ["solution": "a three-month diary study with fifty volunteers"], daysAgo: 8),
        Sample(title: "Plant Pager", category: .sideProject,
               transcript: "A soil sensor that texts me when a plant is actually thirsty instead of on a schedule.",
               fields: [:], daysAgo: 9),
        Sample(title: "Studio Split", category: .business,
               transcript: "Shared studio space booking for illustrators, by the hour, with the good light slots priced higher.",
               fields: ["monetization": "a cut of each booking"], daysAgo: 12),
        Sample(title: "", category: .other,
               transcript: "What if a museum audio guide was written by the people who work nights there, the guards and cleaners, telling you what the rooms are like when nobody's looking",
               fields: [:], daysAgo: 0, checked: false),
    ]

    /// Inserts the samples and saves. Returns the draft (unchecked) idea.
    @discardableResult
    static func seed(into context: ModelContext) -> Idea? {
        var draft: Idea?
        let now = Date.now
        for s in samples {
            let idea = Idea(transcript: s.transcript, title: s.title, category: s.category,
                            createdAt: now.addingTimeInterval(-s.daysAgo * 86_400 - 3_600))
            idea.fields = s.fields
            idea.categoryChosen = s.category != .other
            context.insert(idea)
            if s.checked {
                idea.suggested = true
                if let result = try? PreviewChecker.result(for: idea.intake, rubric: s.category.rubric),
                   let raw = try? result.encoded() {
                    idea.apply(result, raw: raw)
                }
            } else {
                draft = idea
            }
        }
        // The real engine result from `ideacheck "a to-do app for dentists" -b mock --agent`.
        if let url = Bundle.main.url(forResource: "SampleResult", withExtension: "json"),
           let raw = try? Data(contentsOf: url), let result = try? CheckResult.decode(raw) {
            let idea = Idea(transcript: "A to-do app for dentists", title: "Chairside",
                            category: .sideProject, createdAt: now.addingTimeInterval(-5 * 86_400))
            idea.suggested = true
            context.insert(idea)
            idea.apply(result, raw: raw)
        }
        if let draft {
            draft.title = "Night Shift Audio Guide"
            draft.fields = ["audience": "museum visitors who want the building's other life",
                            "solution": "an audio guide written and voiced by the night staff"]
            draft.suggested = true
        }
        try? context.save()
        return draft
    }

    static func previewContainer() -> ModelContainer {
        let container = try! ModelContainer(for: Idea.self, configurations: ModelConfiguration(isStoredInMemoryOnly: true))
        seed(into: container.mainContext)
        return container
    }
}
