import SwiftUI

/// Entry points for what comes after the MVP. Each opens a "coming soon" page
/// that says what it will do, so the idea of it is already in the app.
struct LabView: View {
    struct Feature: Identifiable, Hashable {
        var id: String
        var title: String
        var symbol: String
        var pitch: String
        var detail: String
    }

    static let features: [Feature] = [
        Feature(id: "mixer", title: "Mixer", symbol: "arrow.triangle.merge",
                pitch: "Combine two ideas into a third.",
                detail: "Pick two cards and let the writer propose what they'd be together — then check the mix like any other idea."),
        Feature(id: "trends", title: "Trends & inspiration", symbol: "chart.line.uptrend.xyaxis",
                pitch: "What's moving in the spaces you think about.",
                detail: "Research findings from your checks, gathered by topic, plus prompts drawn from the gaps your ideas keep leaving open."),
        Feature(id: "share", title: "Share", symbol: "square.and.arrow.up.on.square",
                pitch: "Two ideas side by side, as one graphic.",
                detail: "A card-for-card comparison — dimensions, verdicts and ratings — rendered as an image you can post or send."),
    ]

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    Text("Coming next")
                        .font(.sjLabel(.footnote))
                        .textCase(.uppercase)
                        .foregroundStyle(Color.sjMuted)
                    ForEach(Self.features) { f in
                        NavigationLink(value: f) {
                            HStack(spacing: 16) {
                                Image(systemName: f.symbol)
                                    .font(.title2)
                                    .frame(width: 52, height: 52)
                                    .background(Color.sjViolet.opacity(0.18), in: .rect(cornerRadius: 14))
                                    .foregroundStyle(Color.sjViolet)
                                VStack(alignment: .leading, spacing: 4) {
                                    Text(f.title).font(.sjDisplay(.title3)).foregroundStyle(Color.sjText)
                                    Text(f.pitch).font(.subheadline).foregroundStyle(Color.sjMuted)
                                }
                                Spacer()
                                Image(systemName: "chevron.right").foregroundStyle(Color.sjMuted)
                            }
                            .padding(16)
                            .background(Color.sjSurface, in: .rect(cornerRadius: 22, style: .continuous))
                        }
                        .buttonStyle(CardPressStyle())
                    }
                }
                .padding(20)
            }
            .background(Color.sjInk)
            .navigationTitle("Lab")
            .navigationDestination(for: Feature.self) { ComingSoonView(feature: $0) }
        }
    }
}

struct ComingSoonView: View {
    let feature: LabView.Feature

    var body: some View {
        VStack(spacing: 20) {
            Spacer()
            Image(systemName: feature.symbol)
                .font(.system(size: 64, weight: .light))
                .foregroundStyle(Color.sjViolet)
                .symbolEffect(.pulse)
            Text(feature.title).font(.sjDisplay(.largeTitle))
            Text(feature.detail)
                .font(.body)
                .foregroundStyle(Color.sjMuted)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 32)
            Badge(text: "Coming soon", symbol: "hourglass", tint: Color.sjSpark)
            Spacer()
        }
        .frame(maxWidth: .infinity)
        .background(Color.sjInk)
        .navigationBarTitleDisplayMode(.inline)
    }
}
