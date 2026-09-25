import CryptoKit
import Foundation
import Testing
@testable import Sparkjudge

/// A server stand-in for the hosted API: each path answers from a queue (the
/// last answer repeats), after an optional delay, and it records the order
/// requests arrive in and how many were ever in flight at once.
final class HostedStub: URLProtocol, @unchecked Sendable {
    struct Answer: Sendable {
        var status: Int
        var body: String
        var delay: TimeInterval = 0
    }

    struct Seen: Sendable {
        var method: String
        var path: String
        var headers: [String: String]
        var body: Data
    }

    private static let lock = NSLock()
    nonisolated(unsafe) private static var answers: [String: [Answer]] = [:]
    nonisolated(unsafe) private static var seen: [Seen] = []
    nonisolated(unsafe) private static var inFlight = 0
    nonisolated(unsafe) private static var maxInFlight = 0

    static func install(_ a: [String: [Answer]]) {
        lock.withLock { answers = a; seen = []; inFlight = 0; maxInFlight = 0 }
    }

    static var requests: [Seen] { lock.withLock { seen } }
    static var peak: Int { lock.withLock { maxInFlight } }

    static func session() -> URLSession {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [HostedStub.self]
        return URLSession(configuration: c)
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        var body = Data()
        if let stream = request.httpBodyStream {
            stream.open()
            var buf = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable {
                let n = stream.read(&buf, maxLength: buf.count)
                if n <= 0 { break }
                body.append(buf, count: n)
            }
            stream.close()
        }
        let path = request.url?.path() ?? ""
        let answer = Self.lock.withLock { () -> Answer in
            Self.seen.append(Seen(method: request.httpMethod ?? "GET", path: path,
                                  headers: request.allHTTPHeaderFields ?? [:], body: body))
            Self.inFlight += 1
            Self.maxInFlight = max(Self.maxInFlight, Self.inFlight)
            guard var queue = Self.answers[path], let first = queue.first else {
                return Answer(status: 404, body: #"{"status":"error","error":"not_found","message":"no route"}"#)
            }
            if queue.count > 1 { queue.removeFirst(); Self.answers[path] = queue }
            return first
        }
        let respond = { [self] in
            let response = HTTPURLResponse(url: request.url!, statusCode: answer.status, httpVersion: "HTTP/1.1",
                                           headerFields: ["Content-Type": "application/json"])!
            Self.lock.withLock { Self.inFlight -= 1 }
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: Data(answer.body.utf8))
            client?.urlProtocolDidFinishLoading(self)
        }
        if answer.delay > 0 {
            DispatchQueue.global().asyncAfter(deadline: .now() + answer.delay, execute: respond)
        } else {
            respond()
        }
    }

    override func stopLoading() {}
}

/// App Attest for tests: key ids `key-1`, `key-2`…, assertions `sig-1`,
/// `sig-2`… in the order they are made, and a record of every hash signed.
final class FakeAttest: AttestService, @unchecked Sendable {
    private let lock = NSLock()
    private var keys = 0
    private var signatures = 0
    private(set) var attested: [(key: String, hash: Data)] = []
    private(set) var asserted: [(key: String, hash: Data)] = []
    let isSupported: Bool

    init(supported: Bool = true) { isSupported = supported }

    func generateKey() async throws -> String {
        lock.withLock { keys += 1; return "key-\(keys)" }
    }

    func attestKey(_ keyID: String, clientDataHash: Data) async throws -> Data {
        lock.withLock { attested.append((keyID, clientDataHash)) }
        return Data("attestation-of-\(keyID)".utf8)
    }

    func generateAssertion(_ keyID: String, clientDataHash: Data) async throws -> Data {
        lock.withLock {
            signatures += 1
            asserted.append((keyID, clientDataHash))
            return Data("sig-\(signatures)".utf8)
        }
    }
}

@Suite(.serialized)
struct HostedAPITests {
    let base = URL(string: "https://sparkjudge-api.example.com")!
    let user = "0f4c2b1e-9d8a-4c7b-8e6f-5a4b3c2d1e0f"
    static let challenge = Data("thirty-two bytes of challenge!!!".utf8)
    static let me = #"{"user":"0f4c","plan":"free","used":2,"limit":3,"resets_at":"2026-10-01T00:00:00Z"}"#

    private func api(_ attest: FakeAttest, store: MemoryStore = MemoryStore()) -> SparkjudgeAPI {
        SparkjudgeAPI(baseURL: base, auth: .appAttest, user: user, session: HostedStub.session(), attest: attest, store: store)
    }

    private var attestRoutes: [String: [HostedStub.Answer]] {
        ["/v1/attest/challenge": [.init(status: 200, body: #"{"challenge":"\#(Self.challenge.base64EncodedString())","expires_at":"2026-09-25T12:05:00Z"}"#)],
         "/v1/attest": [.init(status: 201, body: #"{"status":"ok"}"#)]]
    }

    // MARK: - The string the server verifies

    @Test func clientDataIsMethodPathQueryAndBodyHash() {
        let url = URL(string: "https://x.example/v1/check?async=1&rubric=business")!
        let s = String(decoding: RequestSigning.clientData(method: "post", url: url, body: Data("{}".utf8)), as: UTF8.self)
        #expect(s == "POST\n/v1/check?async=1&rubric=business\n44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a")

        let get = String(decoding: RequestSigning.clientData(method: "GET", url: URL(string: "https://x.example/v1/me")!, body: nil), as: UTF8.self)
        #expect(get == "GET\n/v1/me\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
        #expect(RequestSigning.emptyBodyHash == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
        #expect(RequestSigning.bodyHash(Data(#"{"idea":"A payroll tool for bakeries"}"#.utf8)) == "3e23d7c8a844ce936cbb382cdf088d92f5d8397921f5347d3c26d761cc5ef814")
    }

    @Test func requestURIKeepsPercentEncodingLikeGo() {
        // Go's r.URL.RequestURI() gives the path and query as sent.
        let url = URL(string: "https://x.example/v1/checks/chk_1/events?q=a%20b&x=%2F")!
        #expect(RequestSigning.requestURI(url) == "/v1/checks/chk_1/events?q=a%20b&x=%2F")
        #expect(RequestSigning.requestURI(URL(string: "https://x.example")!) == "/")
        let hash = RequestSigning.clientDataHash(method: "GET", url: url, body: nil)
        #expect(hash == Data(SHA256.hash(data: RequestSigning.clientData(method: "GET", url: url, body: nil))))
    }

    // MARK: - Attest, then sign

    @Test func attestsOnceThenSignsEachRequest() async throws {
        HostedStub.install(attestRoutes.merging(["/v1/me": [.init(status: 200, body: Self.me)]]) { $1 })
        let attest = FakeAttest()
        let store = MemoryStore()
        let client = api(attest, store: store)

        let plan = try await client.me()
        _ = try await client.me()
        #expect(plan.usageLine == "2 of 3 hosted checks this month")

        let seen = HostedStub.requests
        #expect(seen.map(\.path) == ["/v1/attest/challenge", "/v1/attest", "/v1/me", "/v1/me"])
        #expect(seen.allSatisfy { $0.headers[HostedHeader.user] == user })
        // The attestation is over SHA256 of the challenge's bytes, and sends the challenge back as given.
        #expect(attest.attested.first?.hash == Data(SHA256.hash(data: Self.challenge)))
        let sent = try JSONSerialization.jsonObject(with: seen[1].body) as? [String: String]
        #expect(sent?["key_id"] == "key-1")
        #expect(sent?["challenge"] == Self.challenge.base64EncodedString())
        #expect(sent?["attestation"] == Data("attestation-of-key-1".utf8).base64EncodedString())
        #expect(seen[0].headers[HostedHeader.assertion] == nil)
        #expect(store.read(AttestKeyID.account(for: base)) == "key-1")

        // Each signed request: the key, and an assertion over exactly its method, path and body.
        let me = client.url("v1/me")
        #expect(seen[2].headers[HostedHeader.key] == "key-1")
        #expect(seen[2].headers[HostedHeader.assertion] == Data("sig-1".utf8).base64EncodedString())
        #expect(seen[3].headers[HostedHeader.assertion] == Data("sig-2".utf8).base64EncodedString())
        #expect(attest.asserted.map(\.hash) == Array(repeating: RequestSigning.clientDataHash(method: "GET", url: me, body: nil), count: 2))
    }

    @Test func aPostSignsItsBody() async throws {
        HostedStub.install(attestRoutes.merging(["/v1/check": [.init(status: 202, body: #"{"id":"c","events":"/v1/checks/c/events"}"#)]]) { $1 })
        let attest = FakeAttest()
        let client = api(attest)
        let body = Data(#"{"idea":"A payroll tool for bakeries"}"#.utf8)
        let url = client.url("v1/check", query: [URLQueryItem(name: "async", value: "1")])
        _ = try await client.data("POST", url, body: body)
        #expect(attest.asserted.last?.hash == RequestSigning.clientDataHash(method: "POST", url: url, body: body))
        #expect(HostedStub.requests.last?.body == body)
    }

    // MARK: - Retries

    @Test func aStaleAssertionIsSignedAgainOnce() async throws {
        HostedStub.install(attestRoutes.merging(["/v1/me": [
            .init(status: 401, body: #"{"status":"error","error":"stale_assertion","message":"sign again"}"#),
            .init(status: 200, body: Self.me),
        ]]) { $1 })
        let attest = FakeAttest()
        _ = try await api(attest).me()
        let mes = HostedStub.requests.filter { $0.path == "/v1/me" }
        #expect(mes.count == 2)
        #expect(mes.map { $0.headers[HostedHeader.assertion] } == ["sig-1", "sig-2"].map { Data($0.utf8).base64EncodedString() })
    }

    @Test func aSecondStaleAssertionIsAnError() async throws {
        HostedStub.install(attestRoutes.merging(["/v1/me": [
            .init(status: 401, body: #"{"status":"error","error":"stale_assertion","message":"sign again"}"#),
        ]]) { $1 })
        await #expect(throws: HostedError.unauthorized(code: "stale_assertion")) {
            _ = try await api(FakeAttest()).me()
        }
        #expect(HostedStub.requests.filter { $0.path == "/v1/me" }.count == 2)
    }

    @Test func anUnknownKeyIsReplacedByANewAttestation() async throws {
        HostedStub.install(attestRoutes.merging(["/v1/me": [
            .init(status: 401, body: #"{"status":"error","error":"unknown_key","message":"attest it"}"#),
            .init(status: 200, body: Self.me),
        ]]) { $1 })
        let store = MemoryStore([AttestKeyID.account(for: base): "old-key"])
        _ = try await api(FakeAttest(), store: store).me()
        let seen = HostedStub.requests
        #expect(seen.map(\.path) == ["/v1/me", "/v1/attest/challenge", "/v1/attest", "/v1/me"])
        #expect(seen[0].headers[HostedHeader.key] == "old-key")
        #expect(seen[3].headers[HostedHeader.key] == "key-1")
        #expect(store.read(AttestKeyID.account(for: base)) == "key-1")
    }

    @Test func requestsGoOutOneAtATimeInTheOrderTheyWereSigned() async throws {
        HostedStub.install(attestRoutes.merging(["/v1/me": [.init(status: 200, body: Self.me, delay: 0.03)]]) { $1 })
        let client = api(FakeAttest())
        try await withThrowingTaskGroup(of: Void.self) { group in
            for _ in 0..<6 { group.addTask { _ = try await client.me() } }
            try await group.waitForAll()
        }
        #expect(HostedStub.peak == 1)
        // Counters rise in the order requests arrive: sig-1, sig-2, … on the wire.
        let order = HostedStub.requests.filter { $0.path == "/v1/me" }.compactMap { $0.headers[HostedHeader.assertion] }
            .compactMap { Data(base64Encoded: $0) }.map { String(decoding: $0, as: UTF8.self) }
        #expect(order == (1...6).map { "sig-\($0)" })
    }

    // MARK: - Modes

    @Test func withoutAppAttestReleaseRefusesInsteadOfSkipping() async throws {
        HostedStub.install(["/v1/me": [.init(status: 200, body: Self.me)]])
        await #expect(throws: HostedError.attestUnavailable) {
            _ = try await api(FakeAttest(supported: false)).me()
        }
        #expect(HostedStub.requests.isEmpty)
    }

    @Test func onlyADebugBuildTalkingToADevServerSkipsAttest() {
        let local = URL(string: "http://127.0.0.1:8787")!
        #expect(HostedAuth.resolve(baseURL: local, debugBuild: true) == .userOnly)
        #expect(HostedAuth.resolve(baseURL: local, debugBuild: false) == .appAttest)
        #expect(HostedAuth.resolve(baseURL: URL(string: "https://sparkjudge-api.fly.dev")!, debugBuild: true) == .appAttest)
        #expect(HostedAuth.isDevServer(URL(string: "http://192.168.1.20:8787")!))
        #expect(HostedAuth.isDevServer(URL(string: "http://mac.local:8787")!))
        #expect(!HostedAuth.isDevServer(URL(string: "http://172.32.0.1")!))
        #expect(!HostedAuth.isDevServer(URL(string: "https://10.example.com")!))
    }

    @Test func userOnlySendsTheUserIdAndNoAssertion() async throws {
        HostedStub.install(["/v1/me": [.init(status: 200, body: Self.me)]])
        let attest = FakeAttest()
        let client = SparkjudgeAPI(baseURL: base, auth: .userOnly, user: user, session: HostedStub.session(), attest: attest, store: MemoryStore())
        _ = try await client.me()
        let seen = try #require(HostedStub.requests.first)
        #expect(seen.headers[HostedHeader.user] == user)
        #expect(seen.headers[HostedHeader.key] == nil && seen.headers[HostedHeader.assertion] == nil)
        #expect(attest.asserted.isEmpty)
    }

    @Test func theUserIdIsMadeOnceAndKept() {
        let store = MemoryStore()
        let id = AppUserID.current(in: store)
        #expect(UUID(uuidString: id) != nil)
        #expect(AppUserID.current(in: store) == id)
    }
}

struct HostedPlanTests {
    @Test func decodesMe() throws {
        let pro = try HostedPlan.decode(Data(#"{"user":"0f4c","plan":"pro","used":4,"limit":100,"resets_at":"2026-10-01T00:00:00Z","pro_until":"2026-10-25T19:18:16Z"}"#.utf8))
        #expect(pro.isPro && pro.used == 4 && pro.limit == 100 && pro.remaining == 96)
        #expect(pro.resetsAt == Date(timeIntervalSince1970: 1_790_812_800))
        #expect(pro.proUntil != nil)
        #expect(pro.usageLine == "4 of 100 hosted checks this month")

        let free = try HostedPlan.decode(Data(HostedAPITests.me.utf8))
        #expect(!free.isPro && free.proUntil == nil && free.remaining == 1 && !free.isUsedUp)
    }
}

struct HostedErrorTests {
    private func map(_ status: Int, _ code: String, _ message: String) -> HostedError {
        HostedError.from(status: status, body: Data(#"{"status":"error","error":"\#(code)","message":"\#(message)"}"#.utf8))
    }

    @Test func mapsTheServersRefusals() {
        let free = map(402, "quota_exceeded", "no hosted checks left this month; upgrade to Pro for more")
        #expect(free == .quotaExceeded(upgrade: true, message: "no hosted checks left this month; upgrade to Pro for more"))
        #expect(free.suggestsUpgrade)
        #expect(free.localizedDescription.contains("Pro gives you 100 a month"))

        let pro = map(402, "quota_exceeded", "no hosted checks left this month; they come back on October 1")
        #expect(!pro.suggestsUpgrade)
        #expect(pro.localizedDescription == "You've used this month's hosted checks. They come back on October 1.")

        #expect(map(429, "rate_limited", "slow down") == .rateLimited("slow down"))
        #expect(map(503, "daily_cap", "paused") == .dailyCap)
        #expect(map(503, "daily_cap", "paused").localizedDescription.contains("midnight UTC"))
        #expect(map(401, "bad_assertion", "no") == .unauthorized(code: "bad_assertion"))
        #expect(map(400, "bad_request", "not an intake") == .http(status: 400, code: "bad_request", message: "not an intake"))
        #expect(HostedError.from(status: 502, body: Data("Bad Gateway".utf8)) == .http(status: 502, code: "", message: "Bad Gateway"))
    }
}

@MainActor
struct EntitlementsTests {
    private func fresh() -> Entitlements {
        let defaults = UserDefaults(suiteName: "EntitlementsTests-\(UUID())")!
        return Entitlements(defaults: defaults)
    }

    private func plan(_ name: String, used: Int = 0, limit: Int = 3) -> HostedPlan {
        HostedPlan(user: "u", plan: name, used: used, limit: limit, resetsAt: .now)
    }

    @Test func freeGetsThreeFamiliesAndProAllSix() {
        let e = fresh()
        #expect(!e.isPro)
        #expect(e.cardFamilies == CardFamily.free)
        #expect(!e.allows(.aurora) && e.allows(.nebula))

        e.setStorePro(true) // RevenueCat's entitlement
        #expect(e.cardFamilies == CardFamily.allCases)
        e.setStorePro(false)

        e.setPlan(plan("pro", limit: 100)) // the hosted API heard first
        #expect(e.isPro && e.allows(.contour))
        e.setPlan(plan("free"))
        #expect(!e.isPro)

        e.previewPro = true
        #expect(e.allows(.ripple))
    }

    @Test func theStoresAnswerIsCached() {
        let defaults = UserDefaults(suiteName: "EntitlementsTests-\(UUID())")!
        Entitlements(defaults: defaults).setStorePro(true)
        #expect(Entitlements(defaults: defaults).isPro)
    }

    @Test func aFreeQuotaRefusalOpensThePaywallAndAProOneDoesNot() {
        let e = fresh()
        e.quotaExceeded(upgrade: true)
        #expect(e.paywall == .quota)

        let pro = fresh()
        pro.setStorePro(true)
        pro.quotaExceeded(upgrade: true)
        #expect(pro.paywall == nil)
        pro.quotaExceeded(upgrade: false)
        #expect(pro.paywall == nil)
    }

    @Test func allowanceLineSaysWhatIsLeft() {
        #expect(HostedAllowanceNote.line(plan("free", used: 1)) == "Sparkjudge Cloud: 2 of 3 free hosted checks left this month")
        #expect(HostedAllowanceNote.line(plan("free", used: 3)) == "Free hosted checks used this month. Go Pro for 100 a month")
    }
}
