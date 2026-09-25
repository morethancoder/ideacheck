import Foundation
import RevenueCat
import StoreKit

/// One way to subscribe, as the paywall shows it.
struct ProOption: Identifiable, Sendable, Equatable {
    enum Period: Sendable, Equatable { case month, year, other }

    var id: String          // the store product id
    var title: String
    var price: String       // localized, e.g. "$4.99"
    var period: Period
    /// For a yearly plan: what it comes to per month, localized.
    var perMonth: String?

    var cadence: String {
        switch period {
        case .month: "month"
        case .year: "year"
        case .other: "period"
        }
    }
}

/// The subscription products, the same ids in App Store Connect, RevenueCat
/// and StoreKit/Sparkjudge.storekit.
enum ProProduct {
    static let monthly = "com.morethancoder.sparkjudge.pro.monthly"
    static let yearly = "com.morethancoder.sparkjudge.pro.yearly"
    static let all = [monthly, yearly]
    /// The RevenueCat entitlement the hosted API's webhook watches.
    static let entitlement = "pro"
}

/// Where purchases go. RevenueCat when the build has its key (the hosted API
/// learns about a purchase from RevenueCat's webhook, under the same user id);
/// StoreKit alone otherwise, so the paywall still shows real products.
@MainActor
protocol Storefront: AnyObject {
    /// True when a purchase reaches the hosted API (RevenueCat).
    var isConnected: Bool { get }
    func options() async throws -> [ProOption]
    /// The Pro state after the purchase; nil when the person cancelled.
    func purchase(_ id: String) async throws -> Bool?
    func restore() async throws -> Bool
    func currentPro() async -> Bool
    /// Pro state changes that arrive on their own (renewals, refunds, Ask to Buy).
    func changes() -> AsyncStream<Bool>
}

// MARK: - RevenueCat

@MainActor
final class RevenueCatStorefront: Storefront {
    private var packages: [String: Package] = [:]

    init(apiKey: String, userID: String) {
        if !Purchases.isConfigured {
            #if DEBUG
            Purchases.logLevel = .info
            #endif
            Purchases.configure(with: Configuration.Builder(withAPIKey: apiKey).with(appUserID: userID).build())
        }
    }

    var isConnected: Bool { true }

    func options() async throws -> [ProOption] {
        let offerings = try await Purchases.shared.offerings()
        let available = offerings.current?.availablePackages ?? []
        packages = Dictionary(available.map { ($0.storeProduct.productIdentifier, $0) }, uniquingKeysWith: { a, _ in a })
        return available.map { p in
            let product = p.storeProduct
            let period: ProOption.Period = switch product.subscriptionPeriod?.unit {
            case .month: .month
            case .year: .year
            default: .other
            }
            return ProOption(id: product.productIdentifier, title: product.localizedTitle, price: product.localizedPriceString,
                             period: period, perMonth: period == .year ? product.localizedPricePerMonth : nil)
        }
    }

    func purchase(_ id: String) async throws -> Bool? {
        guard let package = packages[id] else { throw StorefrontError.unknownProduct }
        let result = try await Purchases.shared.purchase(package: package)
        if result.userCancelled { return nil }
        return Self.pro(result.customerInfo)
    }

    func restore() async throws -> Bool {
        Self.pro(try await Purchases.shared.restorePurchases())
    }

    func currentPro() async -> Bool {
        guard let info = try? await Purchases.shared.customerInfo() else { return false }
        return Self.pro(info)
    }

    func changes() -> AsyncStream<Bool> {
        AsyncStream { continuation in
            let task = Task {
                for await info in Purchases.shared.customerInfoStream {
                    continuation.yield(Self.pro(info))
                }
                continuation.finish()
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    nonisolated static func pro(_ info: CustomerInfo) -> Bool {
        info.entitlements[ProProduct.entitlement]?.isActive == true
    }
}

// MARK: - StoreKit 2 alone

/// Used when the build has no RevenueCat key: products and purchases straight
/// from StoreKit (the local .storekit file in the simulator). A purchase here
/// unlocks Pro on this phone, but the hosted API never hears of it.
@MainActor
final class StoreKitStorefront: Storefront {
    private let userID: String
    private var products: [String: Product] = [:]
    private var listener: Task<Void, Never>?
    private var continuations: [UUID: AsyncStream<Bool>.Continuation] = [:]

    init(userID: String) {
        self.userID = userID
        // Transactions that finish outside a purchase call (renewals, Ask to Buy).
        listener = Task { [weak self] in
            for await update in Transaction.updates {
                if case .verified(let transaction) = update { await transaction.finish() }
                guard let self else { return }
                let pro = await self.currentPro()
                for c in self.continuations.values { c.yield(pro) }
            }
        }
    }

    var isConnected: Bool { false }

    func options() async throws -> [ProOption] {
        let found = try await Product.products(for: ProProduct.all)
        products = Dictionary(found.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        return found.sorted { ProProduct.all.firstIndex(of: $0.id) ?? 0 < ProProduct.all.firstIndex(of: $1.id) ?? 0 }.map { p in
            let unit = p.subscription?.subscriptionPeriod.unit
            let period: ProOption.Period = unit == .month ? .month : unit == .year ? .year : .other
            let perMonth = period == .year ? (p.price / 12).formatted(p.priceFormatStyle) : nil
            return ProOption(id: p.id, title: p.displayName, price: p.displayPrice, period: period, perMonth: perMonth)
        }
    }

    func purchase(_ id: String) async throws -> Bool? {
        guard let product = products[id] else { throw StorefrontError.unknownProduct }
        var options: Set<Product.PurchaseOption> = []
        if let token = UUID(uuidString: userID) { options.insert(.appAccountToken(token)) }
        switch try await product.purchase(options: options) {
        case .success(let verification):
            guard case .verified(let transaction) = verification else { throw StorefrontError.unverified }
            await transaction.finish()
            return await currentPro()
        case .userCancelled:
            return nil
        case .pending:
            throw StorefrontError.pending
        @unknown default:
            return nil
        }
    }

    func restore() async throws -> Bool {
        try await AppStore.sync()
        return await currentPro()
    }

    func currentPro() async -> Bool {
        for await entitlement in Transaction.currentEntitlements {
            if case .verified(let t) = entitlement, ProProduct.all.contains(t.productID), t.revocationDate == nil {
                return true
            }
        }
        return false
    }

    func changes() -> AsyncStream<Bool> {
        let id = UUID()
        return AsyncStream { continuation in
            continuations[id] = continuation
            continuation.onTermination = { [weak self] _ in
                Task { @MainActor in self?.continuations[id] = nil }
            }
        }
    }
}

enum StorefrontError: LocalizedError {
    case unknownProduct, unverified, pending

    var errorDescription: String? {
        switch self {
        case .unknownProduct: "That plan isn't available right now. Pull to refresh and try again."
        case .unverified: "The App Store couldn't verify that purchase, so nothing changed."
        case .pending: "The purchase is waiting for approval. Pro turns on as soon as it's approved."
        }
    }
}
