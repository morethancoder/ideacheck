import SwiftUI

/// A card's generated background. `animated` runs the shader's clock (only the
/// focused card in Detail does); otherwise time is frozen at the style's own
/// phase, so SwiftUI draws it once and list scrolling never re-runs it.
struct CardBackground: View {
    var style: CardStyle
    var animated: Bool = false
    /// Unchecked ideas get a near-greyscale version: colors arrive with the verdict.
    var muted: Bool = false

    var body: some View {
        if animated {
            TimelineView(.animation) { context in
                surface(time: style.phase + context.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 3600))
            }
        } else {
            surface(time: style.phase)
                .drawingGroup()
        }
    }

    private func surface(time: Double) -> some View {
        let palette = style.shownPalette(muted: muted).map(Color.init)
        let family = Float(style.family.rawValue)
        let warp = Float(style.warp)
        let offset = CGPoint(x: style.offset.x, y: style.offset.y)
        let scale = Float(style.scale)
        let angle = Float(style.angle)
        return Rectangle()
            .fill(palette[0])
            .visualEffect { content, proxy in
                content.colorEffect(ShaderLibrary.sparkCard(
                    .float2(proxy.size),
                    .float(Float(time)),
                    .float(family),
                    .float(warp),
                    .float2(offset),
                    .float(scale),
                    .float(angle),
                    .color(palette[0]), .color(palette[1]), .color(palette[2]), .color(palette[3])
                ))
            }
            .accessibilityHidden(true)
    }
}

#Preview("Families") {
    ScrollView {
        LazyVGrid(columns: [GridItem(.adaptive(minimum: 150))]) {
            ForEach(CardFamily.allCases) { f in
                CardBackground(style: CardStyle.make(seed: UInt64(f.rawValue) &* 7919, allowed: [f]))
                    .frame(height: 200)
                    .clipShape(.rect(cornerRadius: 20))
                    .overlay(alignment: .bottomLeading) { Text(f.name).padding() }
            }
        }
        .padding()
    }
}
