import SwiftData
import SwiftUI

/// Share for feedback: pick one idea or two, see the graphic, share it as a
/// story (9:16) or a square (1:1) image.
struct ShareView: View {
    @Query(sort: \Idea.createdAt, order: .reverse) private var ideas: [Idea]
    @Environment(Entitlements.self) private var entitlements
    @State private var selection: [UUID]
    @State private var format = UserDefaults.standard.string(forKey: "sjShareFormat").flatMap(ShareFormat.init(rawValue:)) ?? .story
    @State private var image: UIImage?

    /// `initial` picks ideas up front (Detail shares its own idea); empty picks
    /// the two best-rated.
    init(initial: [UUID] = []) {
        _selection = State(initialValue: initial)
    }

    private var cards: [ShareCard] {
        let byID = Dictionary(ideas.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        return selection.compactMap { byID[$0] }.map { ShareCard($0, families: entitlements.cardFamilies) }
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                Picker("Format", selection: $format) {
                    ForEach(ShareFormat.allCases) { Text($0.title).tag($0) }
                }
                .pickerStyle(.segmented)

                preview

                if let image, !cards.isEmpty {
                    ShareLink(item: Image(uiImage: image),
                              preview: SharePreview(cards.count > 1 ? "Which would you build?" : cards[0].title, image: Image(uiImage: image))) {
                        Label(cards.count > 1 ? "Share the pair" : "Share this idea", systemImage: "square.and.arrow.up")
                            .font(.headline)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 6)
                    }
                    .buttonStyle(.borderedProminent)
                    .buttonBorderShape(.capsule)
                    .controlSize(.large)
                    .tint(Color.sjSpark)
                    .foregroundStyle(.black)
                }

                SectionTitle(title: "Pick one or two", symbol: "hand.tap", trailing: "\(selection.count)/2")
                if ideas.isEmpty {
                    ContentUnavailableView("No ideas yet", systemImage: "sparkles", description: Text("Dictate one first."))
                } else {
                    IdeaPicker(ideas: ideas, selection: $selection, limit: 2)
                }
            }
            .padding(20)
        }
        .background(Color.sjInk)
        .navigationTitle("Share for feedback")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear(perform: pickDefault)
        .task(id: RenderKey(cards: cards, format: format)) { render() }
    }

    private struct RenderKey: Hashable {
        var cards: [ShareCard]
        var format: ShareFormat
    }

    @ViewBuilder private var preview: some View {
        let aspect = format.size.width / format.size.height
        Group {
            if let image, !cards.isEmpty {
                Image(uiImage: image).resizable().aspectRatio(aspect, contentMode: .fit)
            } else {
                RoundedRectangle(cornerRadius: 20).fill(Color.sjSurface).aspectRatio(aspect, contentMode: .fit)
                    .overlay { Text("Pick an idea below").font(.sjLabel()).foregroundStyle(Color.sjMuted) }
            }
        }
        .frame(maxHeight: format == .story ? 460 : 340)
        .frame(maxWidth: .infinity)
        .clipShape(.rect(cornerRadius: 20, style: .continuous))
        .shadow(color: .black.opacity(0.3), radius: 16, y: 8)
        .accessibilityLabel(cards.map(\.title).joined(separator: " or "))
    }

    private func pickDefault() {
        guard selection.isEmpty else { return }
        let rated = ideas.filter { $0.composite != nil }.sorted { ($0.composite ?? 0) > ($1.composite ?? 0) }
        selection = Array((rated.isEmpty ? ideas : rated).prefix(2).map(\.id))
    }

    private func render() {
        image = cards.isEmpty ? nil : ShareRenderer.image(cards, format: format)
        if LaunchOptions.current.exportShare, let image, let png = image.pngData() {
            // Screenshots: scripts/screenshots.sh copies this out of the app's container.
            try? png.write(to: URL.documentsDirectory.appending(path: "share-\(format.rawValue).png"))
        }
    }
}
