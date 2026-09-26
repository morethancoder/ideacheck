import SwiftUI

/// A category as a pile of stacked cards with a folder tab.
struct PileView: View {
    static let height: CGFloat = 250

    let category: IdeaCategory
    let ideas: [Idea]

    private let tilts: [Double] = [-7, 5, 0]
    private let shifts: [CGSize] = [CGSize(width: -10, height: 8), CGSize(width: 10, height: 4), .zero]

    var body: some View {
        VStack(spacing: 14) {
            ZStack {
                let top = Array(ideas.prefix(3).reversed())
                ForEach(Array(top.enumerated()), id: \.element.id) { index, idea in
                    let slot = index + (3 - top.count)
                    IdeaCardView(idea: idea, size: .small)
                        .rotationEffect(.degrees(tilts[slot]))
                        .offset(shifts[slot])
                }
            }
            .frame(height: 190)
            HStack(spacing: 6) {
                Image(systemName: category.symbol).foregroundStyle(Color.sjSpark)
                Text(category.title).font(.headline)
                Text("\(ideas.count)")
                    .font(.sjLabel(.caption))
                    .padding(.horizontal, 6).padding(.vertical, 2)
                    .background(Color.sjRaised, in: Capsule())
            }
            .foregroundStyle(Color.sjText)
        }
        .frame(maxWidth: .infinity)
        .frame(height: Self.height)
        .background(Color.sjSurface.opacity(0.6), in: .rect(cornerRadius: 26, style: .continuous))
        .contentShape(.rect(cornerRadius: 26))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(category.title), \(ideas.count) \(ideas.count == 1 ? "idea" : "ideas")")
        .accessibilityHint("Opens the pile")
        .accessibilityAddTraits(.isButton)
    }
}

/// An opened pile: every idea in it, as rows you can tap into.
struct OpenPileView: View {
    let category: IdeaCategory
    let ideas: [Idea]
    var onClose: () -> Void
    @Environment(Entitlements.self) private var entitlements

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Label(category.title, systemImage: category.symbol)
                    .font(.sjDisplay(.title2))
                Spacer()
                Button(action: onClose) {
                    Image(systemName: "xmark")
                        .font(.headline)
                        .padding(10)
                        .background(Color.sjRaised, in: Circle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Close pile")
            }
            .padding(20)
            ScrollView {
                LazyVStack(spacing: 12) {
                    ForEach(ideas) { idea in
                        NavigationLink(value: idea.id) {
                            row(idea)
                        }
                        .buttonStyle(CardPressStyle())
                    }
                }
                .padding(.horizontal, 16)
                .padding(.bottom, 20)
            }
        }
        .foregroundStyle(Color.sjText)
        .background(Color.sjSurface, in: .rect(cornerRadius: 30, style: .continuous))
        .shadow(color: .black.opacity(0.35), radius: 30, y: 12)
    }

    private func row(_ idea: Idea) -> some View {
        HStack(spacing: 14) {
            CardBackground(style: CardStyle.make(seed: idea.seed, allowed: entitlements.cardFamilies), muted: idea.composite == nil)
                .frame(width: 56, height: 72)
                .clipShape(.rect(cornerRadius: 12, style: .continuous))
            VStack(alignment: .leading, spacing: 4) {
                Text(idea.displayTitle).font(.headline).lineLimit(2).multilineTextAlignment(.leading)
                Text(idea.createdAt, format: .relative(presentation: .named))
                    .font(.sjLabel(.caption2))
                    .foregroundStyle(Color.sjMuted)
            }
            Spacer(minLength: 8)
            if let c = idea.composite {
                VStack(alignment: .trailing, spacing: 4) {
                    Text(CheckResult.rating(c), format: .number.precision(.fractionLength(1)))
                        .font(.system(.title2, design: .rounded, weight: .heavy))
                        .monospacedDigit()
                    if let v = idea.verdict { VerdictPill(verdict: v, compact: true) }
                }
            } else {
                Text("Draft").font(.sjLabel(.caption)).foregroundStyle(Color.sjMuted)
            }
            Image(systemName: "chevron.right").font(.footnote).foregroundStyle(Color.sjMuted)
        }
        .padding(10)
        .background(Color.sjRaised.opacity(0.6), in: .rect(cornerRadius: 18, style: .continuous))
        .accessibilityElement(children: .combine)
    }
}
