import Foundation

/// Where an idea came from when it was not dictated: mixed from others in the
/// Lab's Mixer, or started from an item in Trends. Stored on the idea as JSON
/// (`Idea.originData`), so the model stays CloudKit-compatible.
struct IdeaOrigin: Codable, Sendable, Hashable {
    enum Kind: String, Codable, Sendable {
        case mixed
        case trend
    }

    struct Parent: Codable, Sendable, Hashable, Identifiable {
        var id: UUID
        var title: String
    }

    var kind: Kind
    /// The ideas it was mixed from, in the order they were picked.
    var parents: [Parent] = []
    /// The trend item it started from.
    var title: String?
    var url: String?
    var source: String?
    /// Who wrote the mix: "On this iPhone", "Sparkjudge", "Preview".
    var writer: String?

    static func mixed(from ideas: [MixInput], writer: String) -> IdeaOrigin {
        IdeaOrigin(kind: .mixed, parents: ideas.map { Parent(id: $0.id, title: $0.title) }, writer: writer)
    }

    static func trend(_ item: TrendItem) -> IdeaOrigin {
        IdeaOrigin(kind: .trend, title: item.title, url: item.url, source: item.source)
    }

    /// One line for review and detail.
    var line: String {
        switch kind {
        case .mixed: "Mixed from " + parents.map { "“\($0.title)”" }.joined(separator: " + ")
        case .trend: "Started from “\(title ?? "a trend")”" + (source.map { " on \($0)" } ?? "")
        }
    }

    var symbol: String { kind == .mixed ? "arrow.triangle.merge" : "chart.line.uptrend.xyaxis" }
}

extension Idea {
    var origin: IdeaOrigin? {
        get { originData.flatMap { try? JSONDecoder().decode(IdeaOrigin.self, from: $0) } }
        set { originData = newValue.flatMap { try? JSONEncoder().encode($0) } }
    }
}
