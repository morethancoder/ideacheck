import Foundation
import Testing
@testable import Sparkjudge

struct IdeaGroupingTests {
    private let base = Date(timeIntervalSince1970: 1_800_000_000)

    private func idea(_ n: Int, _ c: IdeaCategory, daysAgo: Double, composite: Double? = nil) -> IdeaSummary {
        IdeaSummary(id: UUID(uuidString: String(format: "00000000-0000-0000-0000-%012d", n))!,
                    category: c, createdAt: base.addingTimeInterval(-daysAgo * 86_400), composite: composite)
    }

    @Test func groupsInDisplayOrderAndDropsEmptyCategories() {
        let ideas = [idea(1, .creative, daysAgo: 1), idea(2, .business, daysAgo: 2), idea(3, .other, daysAgo: 0), idea(4, .business, daysAgo: 5)]
        let groups = IdeaGrouping.group(ideas)
        #expect(groups.map(\.category) == [.business, .creative, .other])
        #expect(groups.first?.ids.count == 2)
        #expect(groups.flatMap(\.ids).count == ideas.count)
    }

    @Test func newestFirstWithinAGroup() {
        let ideas = [idea(1, .content, daysAgo: 3), idea(2, .content, daysAgo: 1), idea(3, .content, daysAgo: 2)]
        #expect(IdeaGrouping.group(ideas).first?.ids == [ideas[1].id, ideas[2].id, ideas[0].id])
    }

    @Test func bestRatedPutsUncheckedLast() {
        let ideas = [
            idea(1, .research, daysAgo: 0),                     // draft, newest
            idea(2, .research, daysAgo: 4, composite: 0.8),
            idea(3, .research, daysAgo: 2, composite: 0.3),
            idea(4, .research, daysAgo: 1, composite: 0.8),     // ties 2 → newer first
        ]
        let ids = IdeaGrouping.group(ideas, order: .bestRated).first?.ids
        #expect(ids == [ideas[3].id, ideas[1].id, ideas[2].id, ideas[0].id])
    }

    @Test func emptyInputGivesNoGroups() {
        #expect(IdeaGrouping.group([]).isEmpty)
    }

    @Test func routerChoicesMapOntoCategories() {
        #expect(IdeaCategory(routerChoice: "side_project") == .sideProject)
        #expect(IdeaCategory(routerChoice: "nonsense") == .other)
        #expect(IdeaCategory(routerChoice: nil) == .other)
        #expect(IdeaCategory.other.rubric == nil)
        #expect(IdeaCategory.research.rubric == "research")
        #expect(Set(IdeaCategory.displayOrder) == Set(IdeaCategory.allCases))
    }

    @Test func bundledFieldCatalogMatchesTheEngine() {
        let names = FieldCatalog.shared.idea.map(\.name)
        #expect(names == ["title", "problem", "audience", "solution", "why_now", "monetization", "competitors_known", "differentiation"])
        #expect(FieldCatalog.shared.idea.allSatisfy { !$0.description.isEmpty })
    }
}
