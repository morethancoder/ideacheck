import SwiftUI

/// Placeholders until the pages exist; the App Store requires both links on a
/// subscription screen.
enum LegalLinks {
    static let terms = URL(string: "https://sparkjudge.app/terms")!
    static let privacy = URL(string: "https://sparkjudge.app/privacy")!
}

/// Sparkjudge Pro. It leads with what a check gains (a sharper judge, the web
/// read first, every card style), states the price and the renewal plainly,
/// keeps Close and Restore in view, and never counts down or pre-ticks anything.
struct PaywallView: View {
    var reason: PaywallReason = .browse
    @Environment(Entitlements.self) private var entitlements
    @Environment(\.dismiss) private var dismiss
    @State private var selected: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 26) {
                hero
                if let context = reasonLine {
                    Label(context, systemImage: reason == .quota ? "hourglass" : "lock.open")
                        .font(.callout)
                        .foregroundStyle(Color.sjText)
                        .padding(14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Color.sjSurface, in: .rect(cornerRadius: 16, style: .continuous))
                }
                benefits
                plans
                footer
            }
            .padding(.horizontal, 20)
            .padding(.bottom, 32)
        }
        .scrollIndicators(.hidden)
        .background(Color.sjInk)
        .overlay(alignment: .topTrailing) {
            Button { dismiss() } label: {
                Image(systemName: "xmark")
                    .font(.headline)
                    .frame(width: 36, height: 36)
                    .background(.ultraThinMaterial, in: Circle())
            }
            .foregroundStyle(.white)
            .padding(16)
            .accessibilityLabel("Close")
        }
        .task {
            if entitlements.options.isEmpty { await entitlements.loadOptions() }
            if selected == nil { selected = (entitlements.options.first { $0.period == .year } ?? entitlements.options.first)?.id }
        }
        .onChange(of: entitlements.isPro) { _, pro in if pro { dismiss() } }
    }

    // MARK: - Hero

    private var hero: some View {
        ZStack(alignment: .bottomLeading) {
            CardBackground(style: CardStyle.make(seed: 0xA11_0E4A, allowed: [.aurora]), animated: true)
                .frame(height: 260)
                .mask(LinearGradient(colors: [.black, .black, .black.opacity(0)], startPoint: .top, endPoint: .bottom))
            VStack(alignment: .leading, spacing: 8) {
                Text("SPARKJUDGE PRO")
                    .font(.sjLabel(.caption))
                    .tracking(2)
                    .foregroundStyle(Color.sjSpark)
                Text("Sharper verdicts,\nwith the web read first.")
                    .font(.sjDisplay(.title))
                    .foregroundStyle(Color.sjText)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(.horizontal, 20)
            .padding(.bottom, 4)
        }
        .padding(.horizontal, -20)
    }

    private var reasonLine: String? {
        let plan = entitlements.plan
        switch reason {
        case .quota:
            let resets = plan.map { " They come back on \($0.resetsAt.formatted(.dateTime.month(.wide).day()))." } ?? ""
            let count = plan.map { "all \($0.limit) free hosted checks" } ?? "this month's free hosted checks"
            return "You've used \(count) this month.\(resets) Pro gives you 100 a month."
        case .cloud:
            if let plan, !plan.isPro {
                return "Sparkjudge Cloud is Pro. You can try it with \(plan.remaining) free check\(plan.remaining == 1 ? "" : "s") left this month."
            }
            return "Sparkjudge Cloud is part of Pro. Free accounts get a few hosted checks each month to try it."
        case .styles:
            return "Contour, Ripple and Aurora card styles come with Pro."
        case .browse:
            return nil
        }
    }

    // MARK: - Benefits

    private var benefits: some View {
        VStack(alignment: .leading, spacing: 18) {
            Benefit(symbol: "scope", tint: Verdict.build.color, title: "A sharper judge",
                    detail: "Jev answers every question of a check. It is built for exactly these typed judgments, and it's more accurate than the model on your phone.")
            Benefit(symbol: "globe", tint: Verdict.explore.color, title: "It looks your idea up first",
                    detail: "Before scoring, the check searches the web for competitors and demand, and shows its sources beside the verdict.")
            Benefit(symbol: "square.stack.3d.up.fill", tint: Color.sjViolet, title: "Every card style",
                    detail: "Contour, Ripple and Aurora join Nebula, Cells and Mesh, so each checked idea can roll any of six looks.")
            Benefit(symbol: "gauge.with.dots.needle.67percent", tint: Color.sjSpark, title: "100 hosted checks a month",
                    detail: "Instead of 3. Checks that fail to produce a score are given back.")
        }
    }

    // MARK: - Plans

    @ViewBuilder
    private var plans: some View {
        VStack(spacing: 12) {
            if entitlements.options.isEmpty {
                if let problem = entitlements.optionsProblem {
                    Text(problem).font(.callout).foregroundStyle(Color.sjMuted)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    Button("Try again") { Task { await entitlements.loadOptions() } }
                        .buttonStyle(.bordered)
                } else {
                    ProgressView().frame(maxWidth: .infinity, minHeight: 120)
                }
            } else {
                ForEach(entitlements.options) { option in
                    PlanRow(option: option, selected: selected == option.id, best: option.period == .year && entitlements.options.count > 1)
                        .onTapGesture { selected = option.id }
                        .accessibilityAddTraits(selected == option.id ? [.isButton, .isSelected] : .isButton)
                }
                subscribeButton
                if let chosen {
                    Text("\(chosen.price) a \(chosen.cadence), renewing automatically until you cancel. Cancel any time in Settings → Apple Account → Subscriptions, at least a day before it renews.")
                        .font(.caption)
                        .foregroundStyle(Color.sjMuted)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            if let notice = entitlements.notice {
                Text(notice).font(.callout.weight(.medium)).foregroundStyle(Color.sjText)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    private var chosen: ProOption? { entitlements.options.first { $0.id == selected } }

    private var subscribeButton: some View {
        Button {
            guard let chosen else { return }
            Task { await entitlements.purchase(chosen) }
        } label: {
            Group {
                if entitlements.purchasing != nil {
                    ProgressView().tint(.black)
                } else {
                    Text(chosen.map { "Subscribe for \($0.price) / \($0.cadence)" } ?? "Choose a plan")
                }
            }
            .font(.headline)
            .frame(maxWidth: .infinity)
            .padding(.vertical, 6)
        }
        .buttonStyle(.borderedProminent)
        .buttonBorderShape(.capsule)
        .controlSize(.large)
        .tint(Color.sjSpark)
        .foregroundStyle(.black)
        .disabled(chosen == nil || entitlements.purchasing != nil)
    }

    // MARK: - Footer

    private var footer: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 18) {
                Button {
                    Task { await entitlements.restore() }
                } label: {
                    if entitlements.restoring { ProgressView().controlSize(.small) } else { Text("Restore purchases") }
                }
                Link("Terms", destination: LegalLinks.terms)
                Link("Privacy", destination: LegalLinks.privacy)
            }
            .font(.footnote.weight(.medium))
            .tint(Color.sjMuted)
            Text("Checking on this iPhone stays free and private, with or without Pro.")
                .font(.caption)
                .foregroundStyle(Color.sjMuted)
            if entitlements.storefront?.isConnected == false {
                Label("Developer build: no RevenueCat key (REVENUECAT_API_KEY), so these are StoreKit's products and a purchase unlocks Pro on this phone only. Sparkjudge Cloud won't hear about it.", systemImage: "wrench.and.screwdriver")
                    .font(.caption2)
                    .foregroundStyle(Verdict.park.color)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}

private struct Benefit: View {
    var symbol: String
    var tint: Color
    var title: String
    var detail: String

    var body: some View {
        HStack(alignment: .top, spacing: 14) {
            Image(systemName: symbol)
                .font(.title3)
                .foregroundStyle(tint)
                .frame(width: 30)
            VStack(alignment: .leading, spacing: 3) {
                Text(title).font(.headline).foregroundStyle(Color.sjText)
                Text(detail).font(.subheadline).foregroundStyle(Color.sjMuted)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .accessibilityElement(children: .combine)
    }
}

private struct PlanRow: View {
    var option: ProOption
    var selected: Bool
    var best: Bool

    var body: some View {
        HStack(spacing: 14) {
            Image(systemName: selected ? "largecircle.fill.circle" : "circle")
                .font(.title3)
                .foregroundStyle(selected ? Color.sjSpark : Color.sjMuted)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 8) {
                    Text(option.period == .year ? "Yearly" : option.period == .month ? "Monthly" : option.title)
                        .font(.headline)
                    if best { Badge(text: "Best value", tint: Color.sjSpark) }
                }
                if let perMonth = option.perMonth {
                    Text("\(perMonth) a month, billed yearly").font(.caption).foregroundStyle(Color.sjMuted)
                }
            }
            Spacer()
            Text(option.price).font(.system(.title3, design: .rounded, weight: .bold))
        }
        .padding(16)
        .background(Color.sjSurface, in: .rect(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous)
            .strokeBorder(selected ? Color.sjSpark : Color.sjLine, lineWidth: selected ? 2 : 1))
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
    }
}

/// Presents the paywall whenever `Entitlements.paywall` is set: a quota
/// refusal, a locked choice in Settings.
struct PaywallPresenter: ViewModifier {
    @Environment(Entitlements.self) private var entitlements

    func body(content: Content) -> some View {
        @Bindable var entitlements = entitlements
        content.sheet(item: $entitlements.paywall) { reason in
            PaywallView(reason: reason)
        }
    }
}

extension View {
    func paywallSheet() -> some View { modifier(PaywallPresenter()) }
}

/// One line for the hosted plan: "2 of 3 free hosted checks left this month".
/// Shown in Settings and above a check that will use Sparkjudge Cloud.
struct HostedAllowanceNote: View {
    @Environment(Entitlements.self) private var entitlements

    var body: some View {
        if AppSettings.checker == .remote, let plan = entitlements.plan {
            Label(Self.line(plan), systemImage: plan.isUsedUp ? "exclamationmark.circle" : "cloud")
                .font(.caption2)
                .foregroundStyle(plan.isUsedUp ? Verdict.park.color : Color.sjMuted)
        }
    }

    static func line(_ plan: HostedPlan) -> String {
        if plan.isUsedUp {
            return plan.isPro
                ? "This month's hosted checks are used; they come back on \(plan.resetsAt.formatted(.dateTime.month(.wide).day()))"
                : "Free hosted checks used this month. Go Pro for 100 a month"
        }
        return "Sparkjudge Cloud: \(plan.remaining) of \(plan.limit) \(plan.isPro ? "" : "free ")hosted checks left this month"
    }
}
