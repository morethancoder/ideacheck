import SwiftUI

/// A card's background for exported images when the Metal shader behind
/// `CardBackground` cannot be drawn offscreen (`ShareRenderer` checks). It
/// draws the same seed's look with SwiftUI alone: a mesh gradient over the
/// card's own four colors (`CardStyle.shownPalette`), arranged per family,
/// with soft light in the palette's brightest colors.
struct ExportBackground: View {
    var style: CardStyle
    var muted = false

    /// Exactly the colors the on-screen card uses, darkest first.
    var colors: [OKLCH] { style.shownPalette(muted: muted) }

    /// Which palette color sits at each of the mesh's nine points, row by row.
    var layout: [Int] {
        switch style.family {
        case .nebula: [0, 1, 0, 2, 3, 1, 0, 2, 1]
        case .cells: [1, 0, 2, 0, 3, 0, 2, 0, 1]
        case .mesh: [0, 1, 2, 1, 2, 3, 2, 3, 1]
        case .contour: [0, 1, 1, 1, 2, 3, 0, 1, 2]
        case .ripple: [2, 0, 2, 0, 3, 0, 2, 0, 2]
        case .aurora: [0, 0, 0, 1, 2, 3, 0, 0, 0]
        }
    }

    /// The nine control points: corners fixed, edges slide along their edge,
    /// the middle wanders; all from the seed, so a card exports the same twice.
    var points: [SIMD2<Float>] {
        var rng = SplitMix64(seed: style.seed ^ 0x5EED_CA2D)
        func j(_ amount: Double) -> Float { Float((rng.unit() - 0.5) * 2 * amount) }
        return [
            [0, 0], [0.5 + j(0.2), 0], [1, 0],
            [0, 0.5 + j(0.2)], [0.5 + j(0.22), 0.5 + j(0.22)], [1, 0.5 + j(0.2)],
            [0, 1], [0.5 + j(0.2), 1], [1, 1],
        ]
    }

    /// Soft highlights: position (unit square), radius (fraction of the short side), palette index.
    var glows: [(x: Double, y: Double, r: Double, color: Int)] {
        var rng = SplitMix64(seed: style.seed ^ 0x6106_0000)
        return (0..<4).map { i in (rng.unit(), rng.unit(), 0.25 + rng.unit() * 0.35, i % 2 == 0 ? 3 : 2) }
    }

    var body: some View {
        let c = colors.map(Color.init)
        ZStack {
            MeshGradient(width: 3, height: 3, points: points, colors: layout.map { c[$0] }, smoothsColors: true)
            Canvas { ctx, size in
                ctx.addFilter(.blur(radius: min(size.width, size.height) * 0.12))
                for g in glows {
                    let r = min(size.width, size.height) * g.r
                    let rect = CGRect(x: g.x * size.width - r, y: g.y * size.height - r, width: r * 2, height: r * 2)
                    ctx.fill(Path(ellipseIn: rect), with: .color(c[g.color].opacity(0.35)))
                }
            }
            RadialGradient(colors: [.clear, .black.opacity(0.28)], center: .center, startRadius: 0, endRadius: 420)
        }
        .accessibilityHidden(true)
    }
}
