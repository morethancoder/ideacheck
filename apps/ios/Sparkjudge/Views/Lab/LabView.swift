import SwiftUI

/// The Lab: things to do with ideas once you have a few. Share a pair for
/// feedback, mix ideas into a new one, and find sparks in this week's trends.
struct LabView: View {
    enum Feature: String, Identifiable, Hashable, CaseIterable {
        case share, mixer, trends

        var id: String { rawValue }

        var title: String {
            switch self {
            case .share: "Share for feedback"
            case .mixer: "Mixer"
            case .trends: "Trends & inspiration"
            }
        }

        var symbol: String {
            switch self {
            case .share: "square.and.arrow.up.on.square"
            case .mixer: "arrow.triangle.merge"
            case .trends: "chart.line.uptrend.xyaxis"
            }
        }

        var pitch: String {
            switch self {
            case .share: "Two ideas side by side, as one image: which would they build?"
            case .mixer: "Combine two or three ideas into a new one."
            case .trends: "What launched this week, sorted into kinds of idea, with a prompt for each."
            }
        }
    }

    @State private var path: [Feature] = []

    var body: some View {
        NavigationStack(path: $path) {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    ForEach(Feature.allCases) { f in
                        NavigationLink(value: f) {
                            HStack(spacing: 16) {
                                Image(systemName: f.symbol)
                                    .font(.title2)
                                    .frame(width: 52, height: 52)
                                    .background(Color.sjViolet.opacity(0.18), in: .rect(cornerRadius: 14))
                                    .foregroundStyle(Color.sjViolet)
                                VStack(alignment: .leading, spacing: 4) {
                                    Text(f.title).font(.sjDisplay(.title3)).foregroundStyle(Color.sjText)
                                    Text(f.pitch).font(.subheadline).foregroundStyle(Color.sjMuted).multilineTextAlignment(.leading)
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
            .navigationDestination(for: Feature.self) { f in
                switch f {
                case .share: ShareView()
                case .mixer: MixerView()
                case .trends: TrendsView()
                }
            }
        }
        .onAppear(perform: applyLaunch)
    }

    /// `-sjOpen lab:share|mixer|trends|mix` opens a screen (screenshots).
    private func applyLaunch() {
        guard path.isEmpty, let open = LaunchOptions.current.open, open.hasPrefix("lab:") else { return }
        let name = open.dropFirst(4)
        if name == "mix" {
            // MixerView mixes on its own for lab:mix.
            path = [.mixer]
        } else if let f = Feature(rawValue: String(name)) {
            path = [f]
        }
    }
}
