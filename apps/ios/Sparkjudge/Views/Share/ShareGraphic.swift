import SwiftUI
import UIKit

/// The two shapes a shared image comes in.
enum ShareFormat: String, CaseIterable, Identifiable, Sendable {
    /// 9:16, for stories: 1080 × 1920 pixels.
    case story
    /// 1:1, for feeds and chats: 1080 × 1080 pixels.
    case square

    var id: String { rawValue }
    var title: String { self == .story ? "Story" : "Square" }

    /// The layout's size in points; rendered at `scale`.
    var size: CGSize { self == .story ? CGSize(width: 360, height: 640) : CGSize(width: 360, height: 360) }
    static let scale: CGFloat = 3
    var pixels: CGSize { CGSize(width: size.width * Self.scale, height: size.height * Self.scale) }

    /// A part of the first card, as fractions of the image, below its chips and
    /// above its title: only the card's background shows there.
    var patternBand: CGRect {
        self == .story ? CGRect(x: 0.3, y: 0.18, width: 0.4, height: 0.08) : CGRect(x: 0.08, y: 0.23, width: 0.36, height: 0.05)
    }
}

/// What the graphic shows of one idea. Built from an `Idea` once, so the
/// graphic itself never touches SwiftData.
struct ShareCard: Hashable, Sendable, Identifiable {
    var id: UUID
    var title: String
    var category: IdeaCategory
    var rating: Double?
    var verdict: Verdict?
    /// One line saying why: the summary's first sentence, else the verdict's reason.
    var why: String
    var style: CardStyle
    var muted: Bool

    @MainActor init(_ idea: Idea, families: [CardFamily]) {
        let result = idea.result
        id = idea.id
        title = idea.displayTitle
        category = idea.category
        rating = idea.composite.map(CheckResult.rating)
        verdict = idea.verdict
        why = Self.why(summary: result?.summary, reason: result?.verdictReason, verdict: idea.verdict)
        style = CardStyle.make(seed: idea.seed, allowed: families)
        muted = idea.composite == nil
    }

    init(id: UUID = UUID(), title: String, category: IdeaCategory, rating: Double?, verdict: Verdict?, why: String, style: CardStyle, muted: Bool = false) {
        self.id = id
        self.title = title
        self.category = category
        self.rating = rating
        self.verdict = verdict
        self.why = why
        self.style = style
        self.muted = muted
    }

    static func why(summary: String?, reason: String?, verdict: Verdict?) -> String {
        if let s = summary.flatMap(firstSentence), !s.isEmpty { return s }
        if let r = reason?.trimmingCharacters(in: .whitespacesAndNewlines), !r.isEmpty { return r }
        return verdict?.blurb ?? "Not checked yet"
    }

    static func firstSentence(_ text: String) -> String? {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return nil }
        var first: String?
        t.enumerateSubstrings(in: t.startIndex..., options: .bySentences) { s, _, _, stop in
            first = s?.trimmingCharacters(in: .whitespacesAndNewlines)
            stop = true
        }
        return first ?? t
    }
}

/// The image people post: one or two cards, each with its seeded background,
/// name, category, rating out of 10, verdict and why; the question; the
/// wordmark. Fixed size (`ShareFormat.size`), always dark, and drawn without
/// materials, which `ImageRenderer` leaves out.
struct ShareGraphic: View {
    var cards: [ShareCard]
    var format: ShareFormat
    /// Draw the cards with their Metal shader, as on screen. Off, they get
    /// `ExportBackground`: the same palette drawn by SwiftUI alone.
    var shaders = true

    private var pair: Bool { cards.count > 1 }
    private var question: String { pair ? "Which would you build?" : "Would you build this?" }

    var body: some View {
        ZStack {
            LinearGradient(colors: [Color(red: 0.07, green: 0.06, blue: 0.10), Color(red: 0.02, green: 0.02, blue: 0.03)],
                           startPoint: .top, endPoint: .bottom)
            if format == .story { story } else { square }
        }
        .frame(width: format.size.width, height: format.size.height)
        .environment(\.colorScheme, .dark)
    }

    private var story: some View {
        VStack(alignment: .leading, spacing: 14) {
            wordmark
            if pair {
                ZStack {
                    VStack(spacing: 12) {
                        tile(cards[0], number: 1, tall: false)
                        tile(cards[1], number: 2, tall: false)
                    }
                    orBadge
                }
            } else if let card = cards.first {
                tile(card, number: nil, tall: true)
            }
            prompt(size: 30)
        }
        .padding(.horizontal, 22)
        .padding(.top, 26)
        .padding(.bottom, 22)
    }

    private var square: some View {
        VStack(alignment: .leading, spacing: 10) {
            wordmark
            HStack(spacing: 10) {
                ForEach(Array(cards.prefix(2).enumerated()), id: \.element.id) { i, card in
                    tile(card, number: pair ? i + 1 : nil, tall: true, compact: true)
                        .frame(width: pair ? (format.size.width - 42) / 2 : nil) // equal halves, whatever the text
                }
            }
            .overlay { if pair { orBadge } }
            prompt(size: 21)
        }
        .padding(16)
    }

    private var wordmark: some View {
        HStack(spacing: 6) {
            Image(systemName: "sparkle").foregroundStyle(Color.sjSpark)
            Text("Sparkjudge").font(.system(size: format == .story ? 20 : 15, weight: .heavy, design: .serif)).foregroundStyle(.white)
            Spacer()
            Text(pair ? "idea vs idea" : "idea check").font(.system(size: 10, weight: .medium, design: .monospaced))
                .textCase(.uppercase).foregroundStyle(.white.opacity(0.55))
        }
    }

    private var orBadge: some View {
        Text("or")
            .font(.system(size: 15, weight: .heavy, design: .serif))
            .foregroundStyle(.black)
            .frame(width: 38, height: 38)
            .background(Color.sjSpark, in: Circle())
            .overlay(Circle().strokeBorder(.black.opacity(0.6), lineWidth: 3))
    }

    private func prompt(size: CGFloat) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(question).font(.system(size: size, weight: .bold, design: .serif)).foregroundStyle(.white)
                .lineLimit(1).minimumScaleFactor(0.6)
            Text(pair ? "Reply 1 or 2, and why." : "Reply yes or no, and why.")
                .font(.system(size: size * 0.42, weight: .medium, design: .monospaced))
                .foregroundStyle(.white.opacity(0.6))
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func tile(_ card: ShareCard, number: Int?, tall: Bool, compact: Bool = false) -> some View {
        ZStack(alignment: .bottomLeading) {
            if shaders {
                CardBackground(style: card.style, muted: card.muted)
            } else {
                ExportBackground(style: card.style, muted: card.muted)
            }
            LinearGradient(colors: [.black.opacity(0), .black.opacity(0.7)], startPoint: .center, endPoint: .bottom)
            VStack(alignment: .leading, spacing: compact ? 6 : 8) {
                HStack(spacing: 6) {
                    if let number {
                        Text("\(number)").font(.system(size: 13, weight: .heavy, design: .rounded)).foregroundStyle(.black)
                            .frame(width: 24, height: 24).background(.white, in: Circle())
                    }
                    chip(card.category.badge, symbol: card.category.symbol)
                    Spacer(minLength: 0)
                    if let verdict = card.verdict, !compact { VerdictPill(verdict: verdict).fixedSize() }
                }
                Spacer(minLength: 0)
                Text(card.title)
                    .font(.system(size: compact ? 19 : (tall ? 34 : 27), weight: .bold, design: .serif))
                    .foregroundStyle(.white).lineLimit(compact ? 3 : 2).minimumScaleFactor(0.7)
                    .shadow(color: .black.opacity(0.4), radius: 6)
                HStack(alignment: .lastTextBaseline, spacing: 4) {
                    if let rating = card.rating {
                        Text(rating, format: .number.precision(.fractionLength(1)))
                            .font(.system(size: compact ? 26 : 34, weight: .heavy, design: .rounded)).monospacedDigit().foregroundStyle(.white).lineLimit(1).fixedSize()
                        Text("/10").font(.system(size: 11, weight: .medium, design: .monospaced)).foregroundStyle(.white.opacity(0.7)).fixedSize()
                    }
                    Spacer(minLength: 0)
                    if let verdict = card.verdict, compact { VerdictPill(verdict: verdict, compact: true).fixedSize() }
                }
                Text(card.why)
                    .font(.system(size: compact ? 11 : 13, weight: .regular, design: .serif))
                    .foregroundStyle(.white.opacity(0.88))
                    .lineLimit(compact ? 3 : 2)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(compact ? 12 : 16)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .clipShape(.rect(cornerRadius: compact ? 18 : 24, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: compact ? 18 : 24, style: .continuous).strokeBorder(.white.opacity(0.16), lineWidth: 1))
    }

    private func chip(_ text: String, symbol: String) -> some View {
        HStack(spacing: 4) {
            Image(systemName: symbol).imageScale(.small)
            Text(text)
        }
        .font(.system(size: 10, weight: .medium, design: .monospaced))
        .textCase(.uppercase)
        .lineLimit(1)
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .foregroundStyle(.white)
        .background(.black.opacity(0.35), in: Capsule())
        .overlay(Capsule().strokeBorder(.white.opacity(0.3), lineWidth: 0.5))
    }
}

/// Renders the graphic to an image at 3× (1080 px wide).
///
/// `ImageRenderer` draws the cards' Metal shader (checked on the iOS 26
/// simulator, and pinned by a test), so the export is the card on screen. In
/// case a device's renderer leaves the shader out, which draws a card as its
/// flat ground color, the image is checked where the first card's pattern is
/// and drawn again with `ExportBackground` when it came out flat.
@MainActor
enum ShareRenderer {
    static func image(_ cards: [ShareCard], format: ShareFormat) -> UIImage? {
        let image = render(ShareGraphic(cards: cards, format: format))
        guard let image, !cards.isEmpty, patternIsFlat(image, format: format) else { return image }
        return render(ShareGraphic(cards: cards, format: format, shaders: false))
    }

    static func render(_ graphic: ShareGraphic) -> UIImage? {
        let renderer = ImageRenderer(content: graphic)
        renderer.scale = ShareFormat.scale
        renderer.proposedSize = ProposedViewSize(graphic.format.size)
        renderer.isOpaque = true
        return renderer.uiImage
    }

    /// Whether a band of the first card where only its background shows
    /// (`ShareFormat.patternBand`) is a single color.
    static func patternIsFlat(_ image: UIImage, format: ShareFormat) -> Bool {
        guard let cg = image.cgImage else { return false }
        let w = 48, h = 16
        var px = [UInt8](repeating: 0, count: w * h * 4)
        let drawn: Bool = px.withUnsafeMutableBytes { buf in
            guard let ctx = CGContext(data: buf.baseAddress, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                                      space: CGColorSpace(name: CGColorSpace.sRGB)!, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return false }
            let full = CGSize(width: cg.width, height: cg.height)
            let b = format.patternBand
            let band = CGRect(x: full.width * b.minX, y: full.height * b.minY, width: full.width * b.width, height: full.height * b.height)
            guard let crop = cg.cropping(to: band) else { return false }
            ctx.interpolationQuality = .low
            ctx.draw(crop, in: CGRect(x: 0, y: 0, width: w, height: h))
            return true
        }
        guard drawn else { return false }
        var lo = [255, 255, 255], hi = [0, 0, 0]
        for i in stride(from: 0, to: px.count, by: 4) {
            for c in 0..<3 {
                lo[c] = min(lo[c], Int(px[i + c]))
                hi[c] = max(hi[c], Int(px[i + c]))
            }
        }
        return (0..<3).allSatisfy { hi[$0] - lo[$0] < 12 }
    }
}

#Preview("Story, two") {
    let a = ShareCard(title: "Vector Thread", category: .business, rating: 7.4, verdict: .build,
                      why: "Designers already hand files to agents; this closes the loop.", style: .make(seed: 7))
    let b = ShareCard(title: "The Lighthouse Tapes", category: .creative, rating: 5.8, verdict: .explore,
                      why: "A strong format, with an audience that is easy to find.", style: .make(seed: 99))
    ShareGraphic(cards: [a, b], format: .story)
}
