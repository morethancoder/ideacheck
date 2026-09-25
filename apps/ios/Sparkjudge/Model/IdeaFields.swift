import Foundation

/// One field a caller can send about an idea. The catalogue is not written here:
/// it is `Resources/fields.json`, the output of `ideacheck fields -o json`, so the
/// app, the CLI and the extraction prompt all read one description per field.
struct FieldSpec: Codable, Sendable, Hashable, Identifiable {
    var name: String
    var description: String
    var example: String?

    var id: String { name }

    /// "why_now" → "Why now".
    var label: String { FieldSpec.humanize(name) }

    static func humanize(_ id: String) -> String {
        let spaced = id.replacingOccurrences(of: "_", with: " ")
        return spaced.prefix(1).uppercased() + spaced.dropFirst()
    }
}

struct FieldCatalog: Codable, Sendable {
    var idea: [FieldSpec]
    var profile: [FieldSpec]

    /// Fields the review screen shows. `title` has its own control there.
    var reviewFields: [FieldSpec] { idea.filter { $0.name != "title" } }

    static let shared: FieldCatalog = load(from: .main) ?? fallback

    static func load(from bundle: Bundle) -> FieldCatalog? {
        guard let url = bundle.url(forResource: "fields", withExtension: "json"),
              let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode(FieldCatalog.self, from: data)
    }

    /// Only used if the bundled catalogue is missing, so the form never comes up empty.
    static let fallback = FieldCatalog(
        idea: ["title", "problem", "audience", "solution", "why_now", "monetization", "competitors_known", "differentiation"]
            .map { FieldSpec(name: $0, description: FieldSpec.humanize($0)) },
        profile: []
    )
}
