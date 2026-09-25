import CryptoKit
import DeviceCheck
import Foundation
import Synchronization

/// GET /v1/me: the plan and how much of it is left this month.
struct HostedPlan: Codable, Sendable, Equatable {
    var user: String
    var plan: String
    var used: Int
    var limit: Int
    var resetsAt: Date
    /// When Pro ends (or ended); nil for someone who never subscribed.
    var proUntil: Date?

    enum CodingKeys: String, CodingKey {
        case user, plan, used, limit
        case resetsAt = "resets_at"
        case proUntil = "pro_until"
    }

    var isPro: Bool { plan == "pro" }
    var remaining: Int { max(0, limit - used) }
    var isUsedUp: Bool { used >= limit }

    /// "12 of 100 hosted checks this month"
    var usageLine: String { "\(used) of \(limit) hosted check\(limit == 1 ? "" : "s") this month" }

    static func decode(_ data: Data) throws -> HostedPlan {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .iso8601
        return try d.decode(HostedPlan.self, from: data)
    }
}

/// How requests prove who sends them.
enum HostedAuth: Sendable, Equatable {
    /// App Attest: every request signed by a key Apple vouched for.
    case appAttest
    /// `X-App-User-Id` alone, for a server running with `attest: off`. Only a
    /// Debug build talking to a server on this machine or network uses it.
    case userOnly

    /// Release always attests. Debug skips it only for a dev server, which is
    /// what `make api` runs (and the simulator cannot attest anyway).
    static func resolve(baseURL: URL, debugBuild: Bool = isDebugBuild) -> HostedAuth {
        debugBuild && isDevServer(baseURL) ? .userOnly : .appAttest
    }

    static var isDebugBuild: Bool {
        #if DEBUG
        true
        #else
        false
        #endif
    }

    /// localhost, a .local name or a private IPv4 address.
    static func isDevServer(_ url: URL) -> Bool {
        guard let host = url.host(percentEncoded: false)?.lowercased() else { return false }
        if ["localhost", "127.0.0.1", "::1", "[::1]"].contains(host) || host.hasSuffix(".local") { return true }
        let octets = host.split(separator: ".").compactMap { Int($0) }
        guard octets.count == 4 else { return false }
        switch (octets[0], octets[1]) {
        case (10, _), (127, _), (192, 168): return true
        case (172, 16...31): return true
        default: return false
        }
    }
}

extension Notification.Name {
    /// A hosted request was refused for quota; `object` is the `HostedError`.
    static let hostedQuotaExceeded = Notification.Name("sparkjudge.hostedQuotaExceeded")
    /// A hosted check was taken or given back: /v1/me is worth asking again.
    static let hostedUsageChanged = Notification.Name("sparkjudge.hostedUsageChanged")
}

/// The client for the Sparkjudge hosted API (docs/sparkjudge-api.md).
///
/// Requests go out **one at a time**: the server stores each assertion's
/// counter when the request arrives and refuses a lower one, so two requests
/// signed back to back must not overtake each other. A `stale_assertion` is
/// re-signed and sent once more; a key the server does not know (or no longer
/// accepts) is replaced by a newly attested one, once.
actor SparkjudgeAPI {
    nonisolated let baseURL: URL
    nonisolated let auth: HostedAuth
    nonisolated let user: String
    private let session: URLSession
    private let attest: any AttestService
    private let store: any SecretStore

    private var busy = false
    private var waiters: [CheckedContinuation<Void, Never>] = []

    init(baseURL: URL, auth: HostedAuth? = nil, user: String, session: URLSession = .shared,
         attest: any AttestService = DeviceAttestService(), store: any SecretStore = KeychainStore()) {
        self.baseURL = baseURL
        self.auth = auth ?? HostedAuth.resolve(baseURL: baseURL)
        self.user = user
        self.session = session
        self.attest = attest
        self.store = store
    }

    // MARK: - Shared clients

    private static let clients = Mutex<[URL: SparkjudgeAPI]>([:])

    /// One client per base URL, for the app's user, so every request to a
    /// server waits its turn behind the others.
    static func shared(baseURL: URL) -> SparkjudgeAPI {
        clients.withLock { clients in
            if let c = clients[baseURL] { return c }
            let c = SparkjudgeAPI(baseURL: baseURL, user: AppUserID.current())
            clients[baseURL] = c
            return c
        }
    }

    // MARK: - Routes

    nonisolated func url(_ path: String, query: [URLQueryItem] = []) -> URL {
        var c = URLComponents(url: baseURL.appending(path: path), resolvingAgainstBaseURL: false)!
        if !query.isEmpty { c.queryItems = query }
        return c.url!
    }

    /// GET /v1/me.
    func me() async throws -> HostedPlan {
        try HostedPlan.decode(try await data("GET", url("v1/me")))
    }

    /// A signed request whose answer must be 2xx; returns the body.
    func data(_ method: String, _ url: URL, body: Data? = nil, timeout: TimeInterval = 30) async throws -> Data {
        let session = self.session
        let data = try await perform(method, url, body: body, timeout: timeout, accept: "application/json") { request in
            try await session.data(for: request)
        } errorBody: { $0 }
        if method == "POST", url.path().hasSuffix("/v1/check") {
            NotificationCenter.default.post(name: .hostedUsageChanged, object: nil)
        }
        return data
    }

    /// A signed Server-Sent Events stream: signed once, when it opens. The
    /// turn is released as soon as the response headers arrive.
    func events(_ url: URL) async throws -> URLSession.AsyncBytes {
        let session = self.session
        return try await perform("GET", url, body: nil, timeout: 600, accept: "text/event-stream") { request in
            try await session.bytes(for: request)
        } errorBody: { bytes in
            var out = Data()
            do {
                for try await b in bytes {
                    out.append(b)
                    if out.count >= 8192 { break }
                }
            } catch {}
            return out
        }
    }

    /// GET /v1/healthz, unsigned, for Settings' connection test.
    func health() async -> Result<Void, any Error> {
        do {
            var r = URLRequest(url: url("v1/healthz"))
            r.timeoutInterval = 5
            let (body, response) = try await session.data(for: r)
            if let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) {
                throw HostedError.from(status: http.statusCode, body: body)
            }
            return .success(())
        } catch {
            return .failure(error)
        }
    }

    // MARK: - One at a time

    private func acquire() async {
        if !busy {
            busy = true
            return
        }
        await withCheckedContinuation { waiters.append($0) }
    }

    private func release() {
        if waiters.isEmpty {
            busy = false
        } else {
            waiters.removeFirst().resume() // the turn passes on; busy stays true
        }
    }

    private func perform<R: Sendable>(
        _ method: String, _ url: URL, body: Data?, timeout: TimeInterval, accept: String,
        fetch: @Sendable (URLRequest) async throws -> (R, URLResponse),
        errorBody: @Sendable (R) async -> Data
    ) async throws -> R {
        await acquire()
        defer { release() }
        var resigned = false, reattested = false
        while true {
            try Task.checkCancellation()
            var request = URLRequest(url: url)
            request.httpMethod = method
            request.httpBody = body
            request.timeoutInterval = timeout
            request.setValue(accept, forHTTPHeaderField: "Accept")
            if body != nil { request.setValue("application/json", forHTTPHeaderField: "Content-Type") }
            try await sign(&request, method: method, url: url, body: body)

            let (result, response) = try await fetch(request)
            guard let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) else { return result }
            let error = HostedError.from(status: http.statusCode, body: await errorBody(result))
            if case .unauthorized(let code) = error, auth == .appAttest {
                if code == "stale_assertion", !resigned {
                    resigned = true
                    continue
                }
                if HostedError.reattestCodes.contains(code), !reattested {
                    reattested = true
                    store.delete(keyAccount)
                    continue
                }
            }
            if case .quotaExceeded = error {
                NotificationCenter.default.post(name: .hostedQuotaExceeded, object: error)
            }
            throw error
        }
    }

    // MARK: - App Attest

    private var keyAccount: String { AttestKeyID.account(for: baseURL) }

    /// Adds the identity headers: the user id, and with App Attest the key and
    /// an assertion over exactly this request.
    private func sign(_ request: inout URLRequest, method: String, url: URL, body: Data?) async throws {
        guard auth == .appAttest else {
            HostedHeader.apply(to: &request, user: user, key: nil, assertion: nil)
            return
        }
        guard attest.isSupported else { throw HostedError.attestUnavailable }
        let hash = RequestSigning.clientDataHash(method: method, url: url, body: body)
        var key = try await attestedKey()
        let assertion: Data
        do {
            assertion = try await attest.generateAssertion(key, clientDataHash: hash)
        } catch let error as DCError where error.code == .invalidKey {
            // The key is gone (a restore onto another phone, a reinstall): make a new one.
            store.delete(keyAccount)
            key = try await attestedKey()
            assertion = try await attest.generateAssertion(key, clientDataHash: hash)
        }
        HostedHeader.apply(to: &request, user: user, key: key, assertion: assertion)
    }

    private struct Challenge: Decodable { var challenge: String }
    private struct Attestation: Encodable {
        var key_id: String
        var attestation: String
        var challenge: String
    }

    /// The attested key for this server, attesting a new one when there is none:
    /// generateKey → POST /v1/attest/challenge → attestKey(SHA256(challenge)) → POST /v1/attest.
    private func attestedKey() async throws -> String {
        if let key = store.read(keyAccount) { return key }
        let key = try await attest.generateKey()

        var ask = URLRequest(url: url("v1/attest/challenge"))
        ask.httpMethod = "POST"
        HostedHeader.apply(to: &ask, user: user, key: nil, assertion: nil)
        let (body, response) = try await session.data(for: ask)
        try Self.expect(response, body: body)
        let challenge = try JSONDecoder().decode(Challenge.self, from: body).challenge
        guard let raw = Data(base64Encoded: challenge) else { throw HostedError.http(status: 200, code: "bad_challenge", message: "the challenge is not base64") }

        let object = try await attest.attestKey(key, clientDataHash: Data(SHA256.hash(data: raw)))
        var send = URLRequest(url: url("v1/attest"))
        send.httpMethod = "POST"
        send.setValue("application/json", forHTTPHeaderField: "Content-Type")
        HostedHeader.apply(to: &send, user: user, key: nil, assertion: nil)
        send.httpBody = try JSONEncoder().encode(Attestation(key_id: key, attestation: object.base64EncodedString(), challenge: challenge))
        let (answer, sent) = try await session.data(for: send)
        if (sent as? HTTPURLResponse)?.statusCode != 409 { // key_exists: it is ours already
            try Self.expect(sent, body: answer)
        }
        store.write(key, for: keyAccount)
        return key
    }

    private static func expect(_ response: URLResponse, body: Data) throws {
        guard let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) else { return }
        throw HostedError.from(status: http.statusCode, body: body)
    }
}
