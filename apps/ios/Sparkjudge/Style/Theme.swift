import SwiftUI
import UIKit

/// Sparkjudge's palette and type. Dark first: ink ground, a warm spark accent
/// and a violet counterpoint; the light variants keep the same roles on paper.
extension Color {
    static let sjInk = Color(light: .init(red: 0.965, green: 0.953, blue: 0.933, alpha: 1), dark: .init(red: 0.043, green: 0.039, blue: 0.059, alpha: 1))
    static let sjSurface = Color(light: .init(red: 1, green: 1, blue: 1, alpha: 1), dark: .init(red: 0.086, green: 0.082, blue: 0.110, alpha: 1))
    static let sjRaised = Color(light: .init(red: 0.925, green: 0.914, blue: 0.894, alpha: 1), dark: .init(red: 0.130, green: 0.125, blue: 0.160, alpha: 1))
    static let sjText = Color(light: .init(red: 0.075, green: 0.067, blue: 0.094, alpha: 1), dark: .init(red: 0.957, green: 0.945, blue: 0.925, alpha: 1))
    static let sjMuted = Color(light: .init(red: 0.42, green: 0.40, blue: 0.44, alpha: 1), dark: .init(red: 0.62, green: 0.60, blue: 0.66, alpha: 1))
    static let sjLine = Color(light: .init(white: 0, alpha: 0.10), dark: .init(white: 1, alpha: 0.10))
    static let sjSpark = Color(red: 0.980, green: 0.478, blue: 0.180)
    static let sjViolet = Color(red: 0.49, green: 0.36, blue: 1.0)

    init(light: UIColor, dark: UIColor) {
        self.init(uiColor: UIColor { $0.userInterfaceStyle == .dark ? dark : light })
    }

    init(_ c: OKLCH) {
        let rgb = c.sRGB
        self.init(.sRGB, red: rgb.r, green: rgb.g, blue: rgb.b, opacity: 1)
    }
}

extension Verdict {
    var color: Color {
        switch self {
        case .build: Color(red: 0.24, green: 0.86, blue: 0.59)
        case .explore: Color(red: 0.35, green: 0.78, blue: 0.98)
        case .park: Color(red: 1.0, green: 0.71, blue: 0.28)
        case .kill: Color(red: 1.0, green: 0.35, blue: 0.37)
        case .uncertain: Color(white: 0.7)
        }
    }
}

/// Colors a [0,1] "good news" value: green high, yellow middle, red low.
func goodnessColor(_ v: Double) -> Color {
    switch v {
    case 0.66...: Verdict.build.color
    case 0.4..<0.66: Verdict.park.color
    default: Verdict.kill.color
    }
}

extension Font {
    /// Display type: New York, heavy, scaled with Dynamic Type.
    static func sjDisplay(_ style: Font.TextStyle = .largeTitle) -> Font {
        .system(style, design: .serif, weight: .bold)
    }

    /// Small caps-ish labels: monospaced, for the machine's voice.
    static func sjLabel(_ style: Font.TextStyle = .caption) -> Font {
        .system(style, design: .monospaced, weight: .medium)
    }
}

/// A rounded capsule badge.
struct Badge: View {
    var text: String
    var symbol: String?
    var tint: Color = .white

    var body: some View {
        HStack(spacing: 4) {
            if let symbol { Image(systemName: symbol).imageScale(.small) }
            Text(text)
        }
        .font(.sjLabel(.caption2))
        .textCase(.uppercase)
        .lineLimit(1)
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .foregroundStyle(tint)
        .background(.ultraThinMaterial, in: Capsule())
        .overlay(Capsule().strokeBorder(tint.opacity(0.35), lineWidth: 0.5))
    }
}

/// Section heading used across screens.
struct SectionTitle: View {
    var title: String
    var symbol: String?
    var trailing: String?

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            if let symbol { Image(systemName: symbol).foregroundStyle(Color.sjSpark) }
            Text(title).font(.sjDisplay(.title2))
            Spacer()
            if let trailing { Text(trailing).font(.sjLabel()).foregroundStyle(Color.sjMuted) }
        }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }
}
