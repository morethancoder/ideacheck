import Foundation

/// Checks with Sparkjudge Cloud (the hosted API, `cmd/sparkjudge-api`) or any
/// server that speaks the same contract, `ideacheck serve` included:
/// `POST /v1/check?async=1` returns an id, then `GET /v1/checks/{id}/events`
/// streams `progress` events and ends with one `result` (or `error`) event.
/// Every request goes through `SparkjudgeAPI`, which signs it and sends it in turn.
struct RemoteChecker: IdeaChecker {
    let api: SparkjudgeAPI

    var kind: CheckerKind { .remote }
    var baseURL: URL { api.baseURL }

    init(api: SparkjudgeAPI) {
        self.api = api
    }

    /// A client of its own, for tests and Settings' connection test.
    init(baseURL: URL, session: URLSession = .shared, user: String = AppUserID.current(),
         auth: HostedAuth? = nil, attest: any AttestService = DeviceAttestService(), store: any SecretStore = MemoryStore()) {
        self.api = SparkjudgeAPI(baseURL: baseURL, auth: auth, user: user, session: session, attest: attest, store: store)
    }

    struct Accepted: Decodable {
        var id: String
        var events: String
    }

    func check(_ intake: Intake, rubric: String?) -> AsyncThrowingStream<CheckEvent, any Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    try await run(intake, rubric: rubric, continuation: continuation)
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
                // A check that ended without a score is given back: ask for the plan again.
                NotificationCenter.default.post(name: .hostedUsageChanged, object: nil)
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    func checkURL(rubric: String?) -> URL {
        var items = [URLQueryItem(name: "async", value: "1")]
        if let rubric { items.append(URLQueryItem(name: "rubric", value: rubric)) }
        return api.url("v1/check", query: items)
    }

    private func run(_ intake: Intake, rubric: String?, continuation: AsyncThrowingStream<CheckEvent, any Error>.Continuation) async throws {
        let body = try await api.data("POST", checkURL(rubric: rubric), body: try JSONEncoder().encode(intake))
        let accepted = try JSONDecoder().decode(Accepted.self, from: body)
        continuation.yield(.accepted(id: accepted.id))

        let events = URL(string: accepted.events, relativeTo: baseURL)?.absoluteURL ?? api.url("v1/checks/\(accepted.id)/events")
        let bytes = try await api.events(events)

        var lines = LineSplitter()
        var parser = SSEParser()
        for try await byte in bytes {
            guard let line = lines.push(byte), let message = parser.feed(line: line) else { continue }
            if try handle(message, continuation: continuation) { return }
        }
        if let line = lines.rest(), let message = parser.feed(line: line), try handle(message, continuation: continuation) { return }
        if let message = parser.finish(), try handle(message, continuation: continuation) { return }
        throw CheckError.streamEnded
    }

    /// Returns true when the message ended the check.
    private func handle(_ message: SSEMessage, continuation: AsyncThrowingStream<CheckEvent, any Error>.Continuation) throws -> Bool {
        let data = Data(message.data.utf8)
        switch message.event {
        case "progress":
            if let p = try? JSONDecoder().decode(CheckProgress.self, from: data) {
                continuation.yield(.progress(p))
            }
            return false
        case "result":
            let result = try CheckResult.decode(data)
            continuation.yield(.result(result, raw: data))
            return true
        case "error":
            let body = try? JSONDecoder().decode([String: String].self, from: data)
            throw CheckError.server(body?["error"] ?? message.data)
        default:
            return false
        }
    }

    /// `GET /v1/healthz`, for Settings' "Test connection".
    func health() async -> Result<Void, any Error> {
        await api.health()
    }
}
