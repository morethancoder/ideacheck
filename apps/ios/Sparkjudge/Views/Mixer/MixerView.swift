import SwiftData
import SwiftUI

/// Mixer: pick two or three ideas, and the writer proposes one that combines
/// them. The mix opens in Review as a new draft, linked to where it came from,
/// and is checked like any other idea.
struct MixerView: View {
    @Query(sort: \Idea.createdAt, order: .reverse) private var ideas: [Idea]
    @Environment(Entitlements.self) private var entitlements
    @Environment(AppState.self) private var appState
    @Environment(\.modelContext) private var context
    @State private var selection: [UUID] = []
    @State private var mixing = false
    @State private var error: String?
    /// `-sjOpen lab:mix` (screenshots) mixes the two best ideas once per launch.
    @MainActor private static var autoMixed = false
    private var autoMix: Bool { LaunchOptions.current.open == "lab:mix" && !Self.autoMixed }

    private var mixer: (any IdeaMixer)? { MixerChoice.pick(isPro: entitlements.isPro) }

    private var picked: [Idea] {
        let byID = Dictionary(ideas.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        return selection.compactMap { byID[$0] }
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                header
                if ideas.count < 2 {
                    ContentUnavailableView("Two ideas make a mix", systemImage: "arrow.triangle.merge",
                                           description: Text("Dictate at least two, then come back."))
                } else {
                    SectionTitle(title: "Pick two or three", symbol: "hand.tap", trailing: "\(selection.count)/3")
                    IdeaPicker(ideas: ideas, selection: $selection, limit: 3)
                }
            }
            .padding(20)
            .padding(.bottom, 120)
        }
        .background(Color.sjInk)
        .safeAreaInset(edge: .bottom) { mixBar }
        .navigationTitle("Mixer")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear {
            if selection.isEmpty, autoMix || LaunchOptions.current.open?.hasPrefix("lab:mix") == true {
                let rated = ideas.filter { $0.composite != nil }.sorted { ($0.composite ?? 0) > ($1.composite ?? 0) }
                selection = Array(rated.prefix(2).map(\.id))
            }
            if autoMix {
                Self.autoMixed = true
                Task { await mix() }
            }
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: -18) {
                ForEach(Array(picked.enumerated()), id: \.element.id) { i, idea in
                    CardBackground(style: CardStyle.make(seed: idea.seed, allowed: entitlements.cardFamilies), muted: idea.composite == nil)
                        .frame(width: 64, height: 84)
                        .clipShape(.rect(cornerRadius: 14, style: .continuous))
                        .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Color.sjInk, lineWidth: 3))
                        .rotationEffect(.degrees(Double(i - picked.count / 2) * 8))
                        .zIndex(Double(i))
                }
                if picked.isEmpty {
                    Image(systemName: "arrow.triangle.merge").font(.system(size: 40, weight: .light)).foregroundStyle(Color.sjViolet)
                }
            }
            .frame(height: 96)
            Text(picked.isEmpty ? "What would they be together?" : picked.map(\.displayTitle).joined(separator: " + "))
                .font(.sjDisplay(.title2))
                .foregroundStyle(Color.sjText)
                .lineLimit(3)
            Text("The writer looks for what they share, or what one gives another, and proposes one new idea. You review it before anything is checked.")
                .font(.subheadline)
                .foregroundStyle(Color.sjMuted)
        }
        .animation(.snappy, value: selection)
    }

    private var mixBar: some View {
        VStack(spacing: 6) {
            Button { Task { await mix() } } label: {
                HStack {
                    if mixing { ProgressView().tint(.black) }
                    Label(mixing ? "Mixing…" : "Mix them", systemImage: "wand.and.stars")
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
            .disabled(selection.count < 2 || mixer == nil || mixing)
            Text(error ?? mixer?.label ?? MixerChoice.unavailableReason)
                .font(.caption)
                .foregroundStyle(error == nil ? Color.sjMuted : Verdict.kill.color)
                .multilineTextAlignment(.center)
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 12)
        .background(.bar)
    }

    private func mix() async {
        guard let mixer, selection.count >= 2, !mixing else { return }
        mixing = true
        error = nil
        defer { mixing = false }
        let inputs = picked.map(MixInput.init)
        do {
            let mixed = try await mixer.mix(inputs)
            let draft = mixed.draft(from: inputs, writer: mixer.label)
            context.insert(draft)
            try? context.save()
            selection = []
            appState.reviewing = draft
        } catch {
            self.error = error.localizedDescription
        }
    }
}
