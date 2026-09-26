import Foundation

/// `GET /v1/trends` (internal/sparkjudge/trends.go): the day's items from
/// Show HN, Hacker News, GitHub and news searches, each typed by the judge into
/// the router's idea types, plus one inspiration prompt per type.
struct TrendsReport: Codable, Sendable, Equatable {
    var fetchedAt: String
    var items: [TrendItem]
    var sparks: [TrendSpark]
    var warnings: [String]?
    var stale: Bool?

    enum CodingKeys: String, CodingKey {
        case fetchedAt = "fetched_at"
        case items, sparks, warnings, stale
    }

    /// Go writes RFC 3339 with nanoseconds; tests and samples may not.
    var fetchedDate: Date? {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = f.date(from: fetchedAt) { return d }
        f.formatOptions = [.withInternetDateTime]
        return f.date(from: fetchedAt)
    }

    static func decode(_ data: Data) throws -> TrendsReport {
        try JSONDecoder().decode(TrendsReport.self, from: data)
    }

    /// Items by category, in the app's category order; empty categories left out.
    var groups: [TrendGroup] {
        IdeaCategory.displayOrder.compactMap { category in
            let items = self.items.filter { $0.category == category }
            let spark = sparks.first { IdeaCategory(routerChoice: $0.ideaType) == category }
            return items.isEmpty && spark == nil ? nil : TrendGroup(category: category, spark: spark, items: items)
        }
    }
}

struct TrendItem: Codable, Sendable, Hashable, Identifiable {
    var title: String
    var url: String
    var source: String
    var ideaType: String
    var summary: String?
    var points: Int?

    var id: String { url }
    var category: IdeaCategory { IdeaCategory(routerChoice: ideaType) }

    enum CodingKeys: String, CodingKey {
        case title, url, source
        case ideaType = "idea_type"
        case summary, points
    }
}

struct TrendSpark: Codable, Sendable, Hashable {
    var ideaType: String
    var prompt: String

    enum CodingKeys: String, CodingKey {
        case ideaType = "idea_type"
        case prompt
    }
}

struct TrendGroup: Sendable, Hashable, Identifiable {
    var category: IdeaCategory
    var spark: TrendSpark?
    var items: [TrendItem]
    var id: String { category.rawValue }
}
