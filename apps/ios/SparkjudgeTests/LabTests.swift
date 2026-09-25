import Foundation
import SwiftUI
import Testing
import UIKit
@testable import Sparkjudge

// MARK: - Pixels

/// An image's pixels as RGBA bytes, for sampling.
private struct Pixels {
    var width: Int
    var height: Int
    var bytes: [UInt8]

    init?(_ image: UIImage) {
        guard let cg = image.cgImage else { return nil }
        width = cg.width
        height = cg.height
        bytes = [UInt8](repeating: 0, count: width * height * 4)
        let drawn: Bool = bytes.withUnsafeMutableBytes { buf in
            guard let ctx = CGContext(data: buf.baseAddress, width: width, height: height, bitsPerComponent: 8, bytesPerRow: width * 4,
                                      space: CGColorSpace(name: CGColorSpace.sRGB)!, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return false }
            ctx.draw(cg, in: CGRect(x: 0, y: 0, width: width, height: height))
            return true
        }
        if !drawn { return nil }
    }

    func rgb(_ x: Int, _ y: Int) -> SIMD3<Double> {
        let i = (y * width + x) * 4
        return SIMD3(Double(bytes[i]), Double(bytes[i + 1]), Double(bytes[i + 2])) / 255
    }

    /// The largest distance between any sampled pixel and the first one.
    var spread: Double {
        let first = rgb(0, 0)
        var most = 0.0
        for y in stride(from: 0, to: height, by: max(height / 24, 1)) {
            for x in stride(from: 0, to: width, by: max(width / 24, 1)) {
                let d = rgb(x, y) - first
                most = max(most, (d * d).sum().squareRoot())
            }
        }
        return most
    }
}

@MainActor
private func render<V: View>(_ view: V, size: CGSize, scale: CGFloat = 1) -> UIImage? {
    let r = ImageRenderer(content: view.frame(width: size.width, height: size.height))
    r.scale = scale
    return r.uiImage
}

// MARK: - Share

@MainActor
struct ShareTests {
    private let a = ShareCard(title: "Vector Thread", category: .business, rating: 7.4, verdict: .build,
                              why: "Designers already hand files to agents.", style: .make(seed: 7))
    private let b = ShareCard(title: "The Lighthouse Tapes", category: .creative, rating: 5.8, verdict: .explore,
                              why: "A strong format.", style: .make(seed: 99), muted: false)

    @Test(arguments: ShareFormat.allCases)
    func renderAtTheFormatsPixelSize(format: ShareFormat) throws {
        for cards in [[a, b], [a]] {
            let image = try #require(ShareRenderer.image(cards, format: format))
            let px = try #require(Pixels(image))
            #expect(CGSize(width: px.width, height: px.height) == format.pixels)
            #expect(px.spread > 0.2, "the graphic drew something")
        }
        #expect(ShareFormat.story.pixels == CGSize(width: 1080, height: 1920))
        #expect(ShareFormat.square.pixels == CGSize(width: 1080, height: 1080))
    }

    @Test func whyIsTheSummarysFirstSentenceElseTheReason() {
        #expect(ShareCard.why(summary: "Strong pull from designers. But the market is small.", reason: "x", verdict: .build) == "Strong pull from designers.")
        #expect(ShareCard.why(summary: "  ", reason: "Tarpit gate fired", verdict: .kill) == "Tarpit gate fired")
        #expect(ShareCard.why(summary: nil, reason: nil, verdict: .park) == Verdict.park.blurb)
        #expect(ShareCard.why(summary: nil, reason: nil, verdict: nil) == "Not checked yet")
    }
}

// MARK: - Export background

@MainActor
struct ExportBackgroundTests {
    /// The export draws the card's own four colors: the ones the shader gets.
    @Test func exportPaletteIsTheCardPalette() {
        for seed in (0..<200).map({ UInt64($0) &* 0x9E37_79B9 }) {
            for family in CardFamily.allCases {
                let style = CardStyle.make(seed: seed, allowed: [family])
                for muted in [false, true] {
                    let export = ExportBackground(style: style, muted: muted)
                    #expect(export.colors == style.shownPalette(muted: muted))
                    #expect(export.layout.count == 9 && export.layout.allSatisfy { (0..<4).contains($0) })
                    #expect(Set(export.layout).count >= 3, "\(family) shows most of its palette")
                }
                #expect(style.shownPalette(muted: false) == style.palette)
                #expect(style.shownPalette(muted: true).allSatisfy { $0.c == 0.012 })
            }
        }
    }

    @Test func sameSeedExportsTheSameImage() throws {
        let style = CardStyle.make(seed: 42, allowed: CardFamily.allCases)
        #expect(ExportBackground(style: style).points == ExportBackground(style: style).points)
        #expect(ExportBackground(style: style).points != ExportBackground(style: .make(seed: 43)).points)
        let size = CGSize(width: 60, height: 80)
        let one = try #require(render(ExportBackground(style: style), size: size).flatMap(Pixels.init))
        let two = try #require(render(ExportBackground(style: style), size: size).flatMap(Pixels.init))
        #expect(one.bytes == two.bytes)
        #expect(one.spread > 0.05, "a gradient, not a flat fill")
    }

    /// ImageRenderer draws the Metal card offscreen (it would be its flat
    /// ground color otherwise), so an export is the card on screen.
    @Test func imageRendererDrawsTheShader() throws {
        for family in CardFamily.allCases {
            let style = CardStyle.make(seed: 7, allowed: [family])
            let px = try #require(render(CardBackground(style: style), size: CGSize(width: 60, height: 80)).flatMap(Pixels.init))
            #expect(px.spread > 0.1, "\(family)")
        }
    }

    /// The share renderer sees the pattern in both formats, and would fall
    /// back to ExportBackground on a card drawn flat.
    @Test(arguments: ShareFormat.allCases)
    func flatCardsAreNoticed(format: ShareFormat) throws {
        let cards = [ShareCard(title: "A", category: .business, rating: 7, verdict: .build, why: "x", style: .make(seed: 3)),
                     ShareCard(title: "B", category: .creative, rating: nil, verdict: nil, why: "y", style: .make(seed: 4), muted: true)]
        for shaders in [true, false] {
            let image = try #require(ShareRenderer.render(ShareGraphic(cards: cards, format: format, shaders: shaders)))
            #expect(!ShareRenderer.patternIsFlat(image, format: format), "shaders: \(shaders)")
        }
        let flat = UIGraphicsImageRenderer(size: format.size).image { ctx in
            UIColor(white: 0.1, alpha: 1).setFill()
            ctx.fill(CGRect(origin: .zero, size: format.size))
        }
        #expect(ShareRenderer.patternIsFlat(flat, format: format))
    }
}

// MARK: - Mixer

/// Serves canned replies for the Lab routes; its own state, so it never races
/// the checker tests' stub.
final class LabStub: URLProtocol, @unchecked Sendable {
    private static let lock = NSLock()
    nonisolated(unsafe) private static var replies: [String: (Int, Data)] = [:]
    nonisolated(unsafe) private static var seen: [(URLRequest, Data)] = []

    static func install(_ r: [String: (Int, Data)]) { lock.withLock { replies = r; seen = [] } }
    static var requests: [(URLRequest, Data)] { lock.withLock { seen } }

    static func session() -> URLSession {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [LabStub.self]
        return URLSession(configuration: c)
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        var body = request.httpBody ?? Data()
        if body.isEmpty, let stream = request.httpBodyStream {
            stream.open()
            var buf = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable {
                let n = stream.read(&buf, maxLength: buf.count)
                if n <= 0 { break }
                body.append(buf, count: n)
            }
            stream.close()
        }
        let (status, reply) = Self.lock.withLock { () -> (Int, Data) in
            Self.seen.append((request, body))
            return Self.replies[request.url?.path ?? ""] ?? (404, Data(#"{"error":"no_route","message":"no route"}"#.utf8))
        }
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: reply)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

@Suite(.serialized)
@MainActor
struct MixerTests {
    private let inputs = [
        MixInput(title: "Tide game", idea: "A puzzle game about tides", fields: ["audience": "Sailors", "problem": " "]),
        MixInput(title: "Plumber invoices", idea: "Invoices from a photo of the job"),
    ]

    @Test func requestCarriesTitlesIdeasAndFilledFields() throws {
        let json = try JSONSerialization.jsonObject(with: JSONEncoder().encode(MixRequest(inputs))) as? [String: Any]
        let ideas = try #require(json?["ideas"] as? [[String: Any]])
        #expect(ideas.count == 2)
        #expect(ideas[0]["title"] as? String == "Tide game")
        #expect(ideas[0]["idea"] as? String == "A puzzle game about tides")
        #expect(ideas[0]["fields"] as? [String: String] == ["audience": "Sailors"])
        #expect(ideas[1]["fields"] == nil)
    }

    @Test func responseBecomesAReviewableDraft() throws {
        let body = #"{"fields":{"title":"“Tide Ledger.”","problem":"Harbor plumbers lose jobs to tides","audience":"Plumbers in tidal towns","solution":"An invoice app that plans jobs around the tide table"},"model":"z-ai/glm-5.3-flash"}"#
        let mixed = try JSONDecoder().decode(MixResponse.self, from: Data(body.utf8)).mixed
        #expect(mixed.fields == ["problem": "Harbor plumbers lose jobs to tides", "audience": "Plumbers in tidal towns",
                                 "solution": "An invoice app that plans jobs around the tide table"])
        let draft = mixed.draft(from: inputs, writer: "Sparkjudge")
        #expect(draft.title == "Tide Ledger")
        #expect(draft.transcript == "An invoice app that plans jobs around the tide table. Harbor plumbers lose jobs to tides.")
        #expect(draft.fields["audience"] == "Plumbers in tidal towns")
        #expect(draft.suggested && draft.status == .draft)
        let origin = try #require(draft.origin)
        #expect(origin.kind == .mixed && origin.parents.map(\.id) == inputs.map(\.id) && origin.writer == "Sparkjudge")
        #expect(origin.line == "Mixed from “Tide game” + “Plumber invoices”")
    }

    @Test func remoteMixerPostsToTheLabRoute() async throws {
        LabStub.install(["/v1/mix": (200, Data(#"{"fields":{"title":"Tide Ledger","problem":"p","audience":"a","solution":"s"}}"#.utf8))])
        let client = LabClient(baseURL: URL(string: "http://127.0.0.1:8787")!, session: LabStub.session(), userID: "0f4c8a4e-2b1d-4c3e-9a7f-1e2d3c4b5a61")
        let mixed = try await RemoteMixer(client: client).mix(inputs)
        #expect(mixed == MixedIdea(title: "Tide Ledger", problem: "p", audience: "a", solution: "s"))
        let (request, body) = try #require(LabStub.requests.first)
        #expect(request.httpMethod == "POST")
        #expect(request.value(forHTTPHeaderField: "X-App-User-Id") == "0f4c8a4e-2b1d-4c3e-9a7f-1e2d3c4b5a61")
        #expect(try JSONDecoder().decode(MixRequest.self, from: body) == MixRequest(inputs))
    }

    @Test func aRefusalReadsAsTheServersSentence() async throws {
        LabStub.install(["/v1/mix": (402, Data(#"{"status":"error","error":"pro_required","message":"mixing on the server is part of Pro"}"#.utf8))])
        let client = LabClient(baseURL: URL(string: "http://127.0.0.1:8787")!, session: LabStub.session())
        await #expect(throws: LabClient.ServerError(status: 402, code: "pro_required", message: "mixing on the server is part of Pro")) {
            try await RemoteMixer(client: client).mix(inputs)
        }
    }

    @Test func onDevicePromptComesFromTheBundledFiles() {
        let prompt = OnDeviceMixer.prompt(inputs)
        #expect(prompt.hasPrefix("Combine these ideas into one new idea."))
        #expect(prompt.contains("IDEA 1: Tide game\nA puzzle game about tides\naudience: Sailors"))
        #expect(prompt.contains("IDEA 2: Plumber invoices\nInvoices from a photo of the job"))
        #expect(OnDeviceMixer.instructions().contains("more than their sum"))
    }

    @Test func mixerChoice() async throws {
        struct Ready: IdeaMixer {
            var label = "phone"
            var availability: WriterAvailability = .available
            func mix(_: [MixInput]) async throws -> MixedIdea { MixedIdea(title: "", problem: "", audience: "", solution: "") }
        }
        let server = Ready(label: "server")
        let off = Ready(availability: .unavailable("no"))
        #expect(MixerChoice.pick(isPro: false, checker: .remote, onDevice: Ready(), server: server)?.label == "phone")
        #expect(MixerChoice.pick(isPro: true, checker: .remote, onDevice: off, server: server)?.label == "server")
        #expect(MixerChoice.pick(isPro: false, checker: .remote, onDevice: off, server: server) == nil)
        #expect(MixerChoice.pick(isPro: false, checker: .preview, onDevice: off, server: server) is PreviewMixer)
        let stitched = try await PreviewMixer().mix(inputs)
        #expect(stitched.title == "Tide game × Plumber invoices" && stitched.audience == "Sailors")
    }
}

// MARK: - Trends

@Suite(.serialized)
@MainActor
struct TrendsTests {
    private let body = Data(#"""
    {"fetched_at":"2026-09-25T19:44:42.863013Z","items":[
      {"title":"A game about tides","url":"https://tides.example/","source":"Show HN","idea_type":"creative","points":120},
      {"title":"acme/invoices","url":"https://github.com/acme/invoices","source":"GitHub","idea_type":"business","summary":"Invoices, fast"},
      {"title":"Mystery","url":"https://m.example/","source":"News","idea_type":"something_new"}],
     "sparks":[{"idea_type":"creative","prompt":"What rule could you break?"},{"idea_type":"research","prompt":"What could you measure?"}]}
    """#.utf8)

    @Test func decodesAndGroupsByCategory() throws {
        let r = try TrendsReport.decode(body)
        let fetched = try #require(r.fetchedDate, "Go's RFC 3339 with fractional seconds parses")
        let c = Calendar(identifier: .gregorian).dateComponents(in: .gmt, from: fetched)
        #expect(c.year == 2026 && c.hour == 19 && c.minute == 44 && c.second == 42)
        let groups = r.groups
        #expect(groups.map(\.category) == [.business, .creative, .research, .other])
        #expect(groups.first { $0.category == .creative }?.spark?.prompt == "What rule could you break?")
        #expect(groups.first { $0.category == .research }?.items.isEmpty == true)
        #expect(groups.last?.items.map(\.title) == ["Mystery"])
    }

    @Test func offlineKeepsTheLastReportAndSaysSo() async throws {
        let cache = URL.temporaryDirectory.appending(path: "trends-\(UUID()).json")
        defer { try? FileManager.default.removeItem(at: cache) }
        let body = self.body
        let online = TrendsStore(fetch: { (try TrendsReport.decode(body), body) }, cacheURL: cache)
        await online.refresh()
        #expect(online.report?.items.count == 3 && online.note == nil)

        let offline = TrendsStore(fetch: { throw URLError(.notConnectedToInternet) }, cacheURL: cache)
        #expect(offline.report?.items.count == 3, "the kept report shows before any fetch")
        await offline.refresh()
        #expect(offline.report?.fetchedAt == "2026-09-25T19:44:42.863013Z")
        #expect(offline.note?.hasPrefix("Offline") == true)
    }

    @Test func startingFromATrendRemembersIt() {
        let item = TrendItem(title: "A game about tides", url: "https://tides.example/", source: "Show HN", ideaType: "creative")
        let origin = IdeaOrigin.trend(item)
        #expect(origin.line == "Started from “A game about tides” on Show HN")
        let idea = Idea(transcript: "x")
        idea.origin = origin
        #expect(idea.origin == origin)
    }
}
