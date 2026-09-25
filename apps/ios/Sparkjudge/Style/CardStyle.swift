import Foundation

/// The pattern a card's shader draws. The raw value is what `Card.metal` switches on.
enum CardFamily: Int, CaseIterable, Codable, Sendable, Identifiable {
    case nebula = 0   // domain-warped fbm
    case cells = 1    // voronoi
    case mesh = 2     // gradient mesh
    case contour = 3  // topographic lines over fbm
    case ripple = 4   // interference rings
    case aurora = 5   // flowing curtains

    var id: Int { rawValue }

    var name: String {
        switch self {
        case .nebula: "Nebula"
        case .cells: "Cells"
        case .mesh: "Mesh"
        case .contour: "Contour"
        case .ripple: "Ripple"
        case .aurora: "Aurora"
        }
    }

    /// Free-tier families; the rest are Pro.
    static let free: [CardFamily] = [.nebula, .cells, .mesh]
    var isFree: Bool { CardFamily.free.contains(self) }
}

/// Everything a card background needs, rolled from one seed. The same seed and
/// the same set of allowed families always give the same style.
struct CardStyle: Sendable, Hashable {
    var seed: UInt64
    var family: CardFamily
    /// Four colors, darkest (ground) first.
    var palette: [OKLCH]
    /// Domain-warp strength, 0.2…1.4.
    var warp: Double
    /// Where in the noise field the card sits.
    var offset: SIMD2<Double>
    /// Pattern scale, 1.2…3.2.
    var scale: Double
    /// Rotation of the pattern, radians.
    var angle: Double
    /// Frozen time for static cards, so each card shows a different moment.
    var phase: Double

    /// The seed for an idea: FNV-1a over the UUID's bytes.
    static func seed(for id: UUID) -> UInt64 {
        let u = id.uuid
        return FNV1a.hash([u.0, u.1, u.2, u.3, u.4, u.5, u.6, u.7, u.8, u.9, u.10, u.11, u.12, u.13, u.14, u.15])
    }

    /// Rolls a style. The family is drawn only from `allowed` (the entitlement's
    /// families), so a free user never gets a Pro look.
    static func make(seed: UInt64, allowed: [CardFamily] = CardFamily.free) -> CardStyle {
        var rng = SplitMix64(seed: seed)
        let families = allowed.isEmpty ? CardFamily.free : allowed
        let family = families[Int(rng.next() % UInt64(families.count))]

        // Palette: a base hue plus a harmony (analogous, split, triad or complement).
        let hue = rng.unit() * 360
        let harmonies: [[Double]] = [[0, 28, 56], [0, 150, 210], [0, 120, 240], [0, 180, 30], [0, -35, 40]]
        let harmony = harmonies[Int(rng.next() % UInt64(harmonies.count))]
        let chroma = 0.11 + rng.unit() * 0.1
        let palette = [
            OKLCH(l: 0.16 + rng.unit() * 0.08, c: 0.03 + rng.unit() * 0.04, h: hue + harmony[1] * 0.5),
            OKLCH(l: 0.44 + rng.unit() * 0.1, c: chroma, h: hue + harmony[0]),
            OKLCH(l: 0.64 + rng.unit() * 0.1, c: chroma + 0.02, h: hue + harmony[1]),
            OKLCH(l: 0.84 + rng.unit() * 0.08, c: chroma * 0.8, h: hue + harmony[2]),
        ]
        return CardStyle(
            seed: seed,
            family: family,
            palette: palette.map { OKLCH(l: $0.l, c: $0.c, h: ($0.h.truncatingRemainder(dividingBy: 360) + 360).truncatingRemainder(dividingBy: 360)) },
            warp: 0.2 + rng.unit() * 1.2,
            offset: SIMD2(rng.unit() * 100, rng.unit() * 100),
            scale: 1.2 + rng.unit() * 2.0,
            angle: rng.unit() * .pi * 2,
            phase: rng.unit() * 60
        )
    }
}
