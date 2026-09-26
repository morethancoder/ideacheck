import SwiftUI

/// The idea types ideacheck's router chooses between (configs/rubrics/_router.yaml).
/// The raw values are the router's option keys, so a check result's
/// `idea_type.choice` maps straight onto a case, and a case (except `other`)
/// names the rubric the server should score with.
enum IdeaCategory: String, CaseIterable, Codable, Sendable, Identifiable {
    case business
    case sideProject = "side_project"
    case content
    case research
    case creative
    case other

    var id: String { rawValue }

    /// Parses a router choice, falling back to `other` for anything unknown.
    init(routerChoice: String?) {
        self = routerChoice.flatMap(IdeaCategory.init(rawValue:)) ?? .other
    }

    var title: String {
        switch self {
        case .business: "Business"
        case .sideProject: "Side projects"
        case .content: "Content"
        case .research: "Research"
        case .creative: "Creative"
        case .other: "Unsorted"
        }
    }

    /// Singular label for badges.
    var badge: String {
        switch self {
        case .business: "Business"
        case .sideProject: "Side project"
        case .content: "Content"
        case .research: "Research"
        case .creative: "Creative"
        case .other: "Unsorted"
        }
    }

    var symbol: String {
        switch self {
        case .business: "briefcase.fill"
        case .sideProject: "hammer.fill"
        case .content: "text.bubble.fill"
        case .research: "flask.fill"
        case .creative: "paintpalette.fill"
        case .other: "tray.fill"
        }
    }

    /// The rubric to ask the server for. `other` lets the router decide.
    var rubric: String? { self == .other ? nil : rawValue }

    /// The order categories appear in lists and piles.
    static let displayOrder: [IdeaCategory] = [.business, .sideProject, .creative, .content, .research, .other]
}
