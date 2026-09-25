import Foundation
import Observation

/// Why the paywall is up: it opens on the reason, so it answers the question
/// the person just ran into.
enum PaywallReason: String, Identifiable, Sendable {
    case quota      // this month's hosted checks are used
    case cloud      // chose Sparkjudge Cloud in Settings
    case styles     // tapped a locked card style
    case browse     // opened it from Settings

    var id: String { rawValue }
}

/// What this person may use, and where that comes from:
///
/// - **Pro** is RevenueCat's `pro` entitlement (cached, so it holds offline and
///   at launch), or the hosted API's plan saying `pro` (the webhook got there
///   first), or, in Debug builds only, the "Pro preview" developer switch.
/// - **The hosted plan** is `GET /v1/me`: how many hosted checks are used and
///   allowed this month.
///
/// Pro unlocks hosted checks with Jev and web research (the server enforces
/// that), all six card style families, and whatever Pro gets later.
@MainActor
@Observable
final class Entitlements {
    // MARK: Pro

    /// The store's answer (RevenueCat or StoreKit), cached in UserDefaults.
    private(set) var storePro: Bool {
        didSet { defaults.set(storePro, forKey: Self.cacheKey) }
    }
    /// The Debug-only developer switch.
    var previewPro: Bool {
        didSet { defaults.set(previewPro, forKey: SettingsKey.proPreview) }
    }

    var isPro: Bool { storePro || plan?.isPro == true || previewPro }

    var cardFamilies: [CardFamily] { Entitlements.families(pro: isPro) }

    func allows(_ family: CardFamily) -> Bool { cardFamilies.contains(family) }

    nonisolated static func families(pro: Bool) -> [CardFamily] {
        pro ? CardFamily.allCases : CardFamily.free
    }

    // MARK: Hosted plan

    private(set) var plan: HostedPlan?
    private(set) var planProblem: String?
    private(set) var loadingPlan = false

    // MARK: Purchases

    let storefront: (any Storefront)?
    private(set) var options: [ProOption] = []
    private(set) var optionsProblem: String?
    private(set) var purchasing: String?
    private(set) var restoring = false
    /// One line after a purchase or restore: what happened.
    var notice: String?

    /// Set to show the paywall; RootView presents it.
    var paywall: PaywallReason?

    let userID: String
    private let defaults: UserDefaults
    private let loadPlan: @Sendable () async throws -> HostedPlan
    private var watchers: [Task<Void, Never>] = []
    private var observers: [any NSObjectProtocol] = []

    static let cacheKey = "entitlement.pro"

    /// For tests and previews: nothing starts on its own.
    init(storefront: (any Storefront)? = nil, userID: String = UUID().uuidString.lowercased(),
         defaults: UserDefaults = .standard, previewPro: Bool = false,
         loadPlan: @escaping @Sendable () async throws -> HostedPlan = { throw CancellationError() }) {
        self.storefront = storefront
        self.userID = userID
        self.defaults = defaults
        self.loadPlan = loadPlan
        self.storePro = defaults.bool(forKey: Self.cacheKey)
        self.previewPro = previewPro
    }

    /// The app's: RevenueCat when the build carries its key, else StoreKit;
    /// the plan from the configured server.
    static func live() -> Entitlements {
        let user = AppUserID.current()
        var store: any Storefront = AppSettings.revenueCatAPIKey.map { RevenueCatStorefront(apiKey: $0, userID: user) }
            ?? StoreKitStorefront(userID: user)
        #if DEBUG
        if DebugLaunch.demoPlans { store = DemoStorefront() }
        let preview = UserDefaults.standard.bool(forKey: SettingsKey.proPreview)
        #else
        let preview = false
        #endif
        let e = Entitlements(storefront: store, userID: user, previewPro: preview) {
            try await SparkjudgeAPI.shared(baseURL: AppSettings.serverURL).me()
        }
        e.start()
        return e
    }

    /// Follows the store's changes and the hosted API's usage notices.
    func start() {
        guard watchers.isEmpty else { return }
        if let storefront {
            watchers.append(Task { [weak self] in
                let pro = await storefront.currentPro()
                self?.storePro = pro
                for await pro in storefront.changes() { self?.storePro = pro }
            })
        }
        watchers.append(Task { [weak self] in await self?.refreshPlan() })
        let center = NotificationCenter.default
        observers.append(center.addObserver(forName: .hostedUsageChanged, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { _ = Task { await self?.refreshPlan() } }
        })
        observers.append(center.addObserver(forName: .hostedQuotaExceeded, object: nil, queue: .main) { [weak self] note in
            let upgrade = (note.object as? HostedError)?.suggestsUpgrade ?? false
            MainActor.assumeIsolated { self?.quotaExceeded(upgrade: upgrade) }
        })
    }

    /// A check was refused for quota: show the paywall when Pro would help.
    func quotaExceeded(upgrade: Bool) {
        if upgrade && !isPro { paywall = .quota }
        Task { await refreshPlan() }
    }

    func refreshPlan() async {
        loadingPlan = true
        defer { loadingPlan = false }
        do {
            plan = try await loadPlan()
            planProblem = nil
        } catch is CancellationError {
        } catch let error as HostedError {
            planProblem = error.localizedDescription
        } catch {
            planProblem = "Sparkjudge Cloud isn't reachable right now."
        }
    }

    // MARK: - Buying

    func loadOptions() async {
        guard let storefront else {
            optionsProblem = "Purchases aren't available in this build."
            return
        }
        do {
            options = try await storefront.options()
            optionsProblem = options.isEmpty ? "No plans are on sale right now. Try again later." : nil
        } catch {
            optionsProblem = "The App Store didn't answer: \(error.localizedDescription)"
        }
    }

    func purchase(_ option: ProOption) async {
        guard let storefront, purchasing == nil else { return }
        purchasing = option.id
        defer { purchasing = nil }
        do {
            guard let pro = try await storefront.purchase(option.id) else { return } // cancelled: say nothing
            storePro = pro
            notice = pro ? "Welcome to Pro." : "The purchase went through, but Pro isn't active yet."
            if pro { paywall = nil }
            await refreshPlan()
        } catch {
            notice = error.localizedDescription
        }
    }

    func restore() async {
        guard let storefront, !restoring else { return }
        restoring = true
        defer { restoring = false }
        do {
            let pro = try await storefront.restore()
            storePro = pro
            notice = pro ? "Pro restored." : "No active Pro subscription was found for this Apple ID."
            await refreshPlan()
        } catch {
            notice = error.localizedDescription
        }
    }

    /// For tests: what the store says now.
    func setStorePro(_ pro: Bool) { storePro = pro }
    /// For tests: a plan as /v1/me answered it.
    func setPlan(_ plan: HostedPlan?) { self.plan = plan }
}
