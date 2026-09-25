import Foundation

extension CardStyle {
    /// The four colors a card is drawn with: the style's palette, or for an
    /// unchecked idea a near-greyscale version of it (colors arrive with the
    /// verdict). The shader and the exported image both read this, so a shared
    /// card is the card on screen.
    func shownPalette(muted: Bool) -> [OKLCH] {
        muted ? palette.map { OKLCH(l: $0.l * 0.85, c: 0.012, h: $0.h) } : palette
    }
}
