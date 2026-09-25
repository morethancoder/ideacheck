#if DEBUG
import SwiftData
import SwiftUI

/// Debug-only launch arguments for trying the paid tier in the simulator,
/// where nothing can be tapped from a script:
///
/// | Argument | Effect |
/// |---|---|
/// | `-sjPaywall quota\|cloud\|styles\|browse` | open the paywall on launch |
/// | `-sjDemoPlans YES` | the paywall lists two sample plans (no StoreKit file outside Xcode) |
/// | `-sjHostedChecks N` | check the N newest ideas one after another with the chosen checker |
enum DebugLaunch {
    static var paywall: PaywallReason? {
        UserDefaults.standard.string(forKey: "sjPaywall").flatMap(PaywallReason.init(rawValue:))
    }

    static var demoPlans: Bool { UserDefaults.standard.bool(forKey: "sjDemoPlans") }

    static var hostedChecks: Int { UserDefaults.standard.integer(forKey: "sjHostedChecks") }
}

/// Sample plans for screenshots; buying one only says it is a demo.
@MainActor
final class DemoStorefront: Storefront {
    var isConnected: Bool { true }

    func options() async throws -> [ProOption] {
        [ProOption(id: ProProduct.monthly, title: "Pro Monthly", price: "$4.99", period: .month),
         ProOption(id: ProProduct.yearly, title: "Pro Yearly", price: "$39.99", period: .year, perMonth: "$3.33")]
    }

    func purchase(_ id: String) async throws -> Bool? { throw StorefrontError.pending }
    func restore() async throws -> Bool { false }
    func currentPro() async -> Bool { false }
    func changes() -> AsyncStream<Bool> { AsyncStream { _ in } }
}

struct DebugLaunchHooks: ViewModifier {
    @Environment(Entitlements.self) private var entitlements
    @Environment(CheckCoordinator.self) private var coordinator
    @Environment(\.modelContext) private var context

    func body(content: Content) -> some View {
        content.task {
            if let reason = DebugLaunch.paywall {
                try? await Task.sleep(for: .milliseconds(600))
                entitlements.paywall = reason
            }
            let count = DebugLaunch.hostedChecks
            guard count > 0 else { return }
            let ideas = (try? context.fetch(FetchDescriptor<Idea>(sortBy: [SortDescriptor(\.createdAt, order: .reverse)]))) ?? []
            for idea in ideas.prefix(count) {
                coordinator.check(idea, in: context)
                while coordinator.isChecking(idea) { try? await Task.sleep(for: .milliseconds(200)) }
            }
        }
    }
}
#endif
