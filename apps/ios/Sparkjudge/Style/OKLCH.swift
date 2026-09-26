import Foundation

/// A color in OKLCH (Björn Ottosson's Oklab in polar form): perceptual
/// lightness `l` in [0,1], chroma `c` (0…~0.37), hue `h` in degrees. Palettes are
/// built here because equal steps in OKLCH look like equal steps.
struct OKLCH: Sendable, Hashable, Codable {
    var l: Double
    var c: Double
    var h: Double

    /// Linear-light → gamma-encoded sRGB components in [0,1]. Out-of-gamut
    /// colors lose chroma until they fit, so the hue stays put.
    var sRGB: (r: Double, g: Double, b: Double) {
        var chroma = c
        for _ in 0..<24 {
            let rgb = OKLCH.toLinearSRGB(l: l, c: chroma, h: h)
            if [rgb.0, rgb.1, rgb.2].allSatisfy({ $0 >= -0.0001 && $0 <= 1.0001 }) {
                return (OKLCH.encode(rgb.0), OKLCH.encode(rgb.1), OKLCH.encode(rgb.2))
            }
            chroma *= 0.85
        }
        let rgb = OKLCH.toLinearSRGB(l: l, c: 0, h: h)
        return (OKLCH.encode(rgb.0), OKLCH.encode(rgb.1), OKLCH.encode(rgb.2))
    }

    static func toLinearSRGB(l: Double, c: Double, h: Double) -> (Double, Double, Double) {
        let hr = h * .pi / 180
        let a = c * cos(hr), b = c * sin(hr)
        let l_ = l + 0.3963377774 * a + 0.2158037573 * b
        let m_ = l - 0.1055613458 * a - 0.0638541728 * b
        let s_ = l - 0.0894841775 * a - 1.2914855480 * b
        let l3 = l_ * l_ * l_, m3 = m_ * m_ * m_, s3 = s_ * s_ * s_
        return (
            4.0767416621 * l3 - 3.3077115913 * m3 + 0.2309699292 * s3,
            -1.2684380046 * l3 + 2.6097574011 * m3 - 0.3413193965 * s3,
            -0.0041960863 * l3 - 0.7034186147 * m3 + 1.7076147010 * s3
        )
    }

    static func encode(_ x: Double) -> Double {
        let v = min(max(x, 0), 1)
        return v <= 0.0031308 ? 12.92 * v : 1.055 * pow(v, 1 / 2.4) - 0.055
    }
}
