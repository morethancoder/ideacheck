import Foundation

/// What the list and pile views need to know about an idea to place it. Kept
/// free of SwiftData so grouping is a pure function that tests can call.
struct IdeaSummary: Sendable, Hashable, Identifiable {
    var id: UUID
    var category: IdeaCategory
    var createdAt: Date
    var composite: Double?
}

struct IdeaGroup: Sendable, Hashable, Identifiable {
    var category: IdeaCategory
    var ids: [UUID]
    var id: IdeaCategory { category }
}

enum IdeaGrouping {
    enum Order: String, CaseIterable, Sendable {
        case newest
        case bestRated
    }

    /// Groups ideas by category in `IdeaCategory.displayOrder`, dropping empty
    /// categories. Within a group: newest first, or best-rated first with
    /// unchecked ideas after the checked ones (newest first among themselves).
    static func group(_ ideas: [IdeaSummary], order: Order = .newest) -> [IdeaGroup] {
        let buckets = Dictionary(grouping: ideas, by: \.category)
        return IdeaCategory.displayOrder.compactMap { category in
            guard let members = buckets[category], !members.isEmpty else { return nil }
            let sorted = members.sorted { a, b in
                if order == .bestRated {
                    switch (a.composite, b.composite) {
                    case let (x?, y?) where x != y: return x > y
                    case (.some, nil): return true
                    case (nil, .some): return false
                    default: break
                    }
                }
                if a.createdAt != b.createdAt { return a.createdAt > b.createdAt }
                return a.id.uuidString < b.id.uuidString
            }
            return IdeaGroup(category: category, ids: sorted.map(\.id))
        }
    }
}

extension Idea {
    var summary: IdeaSummary {
        IdeaSummary(id: id, category: category, createdAt: createdAt, composite: composite)
    }
}
