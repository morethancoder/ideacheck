import Foundation
import Observation

/// What this person may use. No StoreKit yet: `isPro` is false unless the
/// "Pro preview" developer switch in Settings is on. When purchases land, only
/// this type learns where `isPro` comes from.
@MainActor
@Observable
final class Entitlements {
    var isPro: Bool

    init(isPro: Bool = UserDefaults.standard.bool(forKey: SettingsKey.proPreview)) {
        self.isPro = isPro
    }

    var cardFamilies: [CardFamily] { Entitlements.families(pro: isPro) }

    func allows(_ family: CardFamily) -> Bool { cardFamilies.contains(family) }

    nonisolated static func families(pro: Bool) -> [CardFamily] {
        pro ? CardFamily.allCases : CardFamily.free
    }
}
