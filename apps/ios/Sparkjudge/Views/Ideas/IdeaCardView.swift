import SwiftUI

/// An idea as a card: generated background, name, category badge, rating out
/// of 10 and the verdict word. Static unless `animated`.
struct IdeaCardView: View {
    enum Size {
        case small, regular, hero

        var width: CGFloat? {
            switch self {
            case .small: 132
            case .regular: 210
            case .hero: nil
            }
        }

        var height: CGFloat {
            switch self {
            case .small: 176
            case .regular: 280
            case .hero: 380
            }
        }
    }

    let idea: Idea
    var size: Size = .regular
    var animated = false
    @Environment(Entitlements.self) private var entitlements
    @Environment(CheckCoordinator.self) private var coordinator
    @ScaledMetric(relativeTo: .largeTitle) private var ratingSize: CGFloat = 34

    private var style: CardStyle { CardStyle.make(seed: idea.seed, allowed: entitlements.cardFamilies) }
    private var corner: CGFloat { size == .small ? 18 : 28 }

    var body: some View {
        ZStack(alignment: .bottomLeading) {
            CardBackground(style: style, animated: animated, muted: idea.composite == nil)
            LinearGradient(colors: [.black.opacity(0), .black.opacity(0.55)], startPoint: .center, endPoint: .bottom)
            content
                .padding(size == .small ? 12 : 18)
        }
        .frame(width: size.width, height: size.height)
        .frame(maxWidth: size == .hero ? .infinity : nil)
        .clipShape(.rect(cornerRadius: corner, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: corner, style: .continuous).strokeBorder(.white.opacity(0.14), lineWidth: 1))
        .shadow(color: .black.opacity(0.25), radius: size == .small ? 6 : 14, y: 8)
        .environment(\.colorScheme, .dark) // text sits on the shader, always light
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibilityText)
        .accessibilityAddTraits(.isButton)
    }

    @ViewBuilder private var content: some View {
        VStack(alignment: .leading, spacing: size == .small ? 6 : 10) {
            HStack(spacing: 6) {
                if size != .small { Badge(text: idea.category.badge, symbol: idea.category.symbol) }
                Spacer(minLength: 0)
                statusBadge
            }
            Spacer(minLength: 0)
            Text(idea.displayTitle)
                .font(size == .small ? .sjDisplay(.headline) : .sjDisplay(size == .hero ? .largeTitle : .title2))
                .foregroundStyle(.white)
                .lineLimit(size == .small ? 3 : 3)
                .minimumScaleFactor(0.7)
                .shadow(color: .black.opacity(0.4), radius: 6)
            if let composite = idea.composite {
                rating(composite)
            }
        }
    }

    @ViewBuilder private var statusBadge: some View {
        if let run = coordinator.runs[idea.id] {
            HStack(spacing: 6) {
                ProgressView(value: run.fraction).progressViewStyle(.circular).controlSize(.mini).tint(.white)
                if size != .small { Text("Checking").font(.sjLabel(.caption2)).textCase(.uppercase) }
            }
            .padding(.horizontal, 8).padding(.vertical, 4)
            .background(.ultraThinMaterial, in: Capsule())
        } else if idea.status == .failed {
            Badge(text: size == .small ? "!" : "Check failed", symbol: "exclamationmark.triangle.fill", tint: Verdict.park.color)
        } else if idea.composite == nil, size != .small {
            Badge(text: "Draft", symbol: "sparkle")
        }
    }

    private func rating(_ composite: Double) -> some View {
        let verdict = idea.verdict
        return HStack(alignment: .lastTextBaseline, spacing: 6) {
            Text(CheckResult.rating(composite), format: .number.precision(.fractionLength(1)))
                .font(.system(size: size == .small ? ratingSize * 0.55 : (size == .hero ? ratingSize * 1.4 : ratingSize), weight: .heavy, design: .rounded))
                .monospacedDigit()
                .lineLimit(1)
                .fixedSize()
                .foregroundStyle(.white)
            Text("/10").font(.sjLabel(.caption)).foregroundStyle(.white.opacity(0.7))
            Spacer(minLength: 0)
            if let verdict {
                VerdictPill(verdict: verdict, compact: size == .small)
                    .fixedSize()
            } else if let raw = idea.verdictRaw {
                Badge(text: raw)
            }
        }
    }

    private var accessibilityText: String {
        var parts = [idea.displayTitle, idea.category.badge]
        if let c = idea.composite {
            parts.append(String(format: "rated %.1f out of 10", CheckResult.rating(c)))
            if let v = idea.verdict { parts.append("verdict \(v.word)") }
        } else if coordinator.runs[idea.id] != nil {
            parts.append("checking")
        } else {
            parts.append("not checked yet")
        }
        return parts.joined(separator: ", ")
    }
}

/// The verdict word, styled per verdict.
struct VerdictPill: View {
    var verdict: Verdict
    var compact = false

    var body: some View {
        HStack(spacing: 4) {
            if !compact { Image(systemName: verdict.symbol).imageScale(.small) }
            Text(verdict.word)
        }
        .font(.system(compact ? .caption2 : .subheadline, design: .rounded, weight: .heavy))
        .textCase(.uppercase)
        .padding(.horizontal, compact ? 6 : 10)
        .padding(.vertical, compact ? 3 : 5)
        .foregroundStyle(verdict == .kill ? .white : .black)
        .background(verdict.color, in: Capsule())
        .overlay {
            if verdict == .kill { Capsule().strokeBorder(.white.opacity(0.5), lineWidth: 1) }
        }
        .accessibilityLabel("Verdict: \(verdict.word)")
    }
}
