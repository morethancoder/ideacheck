import Foundation

/// Checks through an ideacheck server (`ideacheck serve`):
/// `POST /v1/check?async=1` returns an id, then `GET /v1/checks/{id}/events`
/// streams `progress` events and ends with one `result` (or `error`) event.
struct RemoteChecker: IdeaChecker {
    let baseURL: URL
    var session: URLSession = .shared

    var kind: CheckerKind { .remote }

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
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    func checkURL(rubric: String?) -> URL {
        var c = URLComponents(url: baseURL.appending(path: "v1/check"), resolvingAgainstBaseURL: false)!
        var items = [URLQueryItem(name: "async", value: "1")]
        if let rubric { items.append(URLQueryItem(name: "rubric", value: rubric)) }
        c.queryItems = items
        return c.url!
    }

    private func run(_ intake: Intake, rubric: String?, continuation: AsyncThrowingStream<CheckEvent, any Error>.Continuation) async throws {
        var post = URLRequest(url: checkURL(rubric: rubric))
        post.httpMethod = "POST"
        post.setValue("application/json", forHTTPHeaderField: "Content-Type")
        post.httpBody = try JSONEncoder().encode(intake)
        post.timeoutInterval = 30

        let (body, response) = try await session.data(for: post)
        try Self.expect(response, body: body, codes: [200, 202])
        let accepted = try JSONDecoder().decode(Accepted.self, from: body)
        continuation.yield(.accepted(id: accepted.id))

        var get = URLRequest(url: URL(string: accepted.events, relativeTo: baseURL)?.absoluteURL ?? baseURL.appending(path: "v1/checks/\(accepted.id)/events"))
        get.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        get.timeoutInterval = 600
        let (bytes, streamResponse) = try await session.bytes(for: get)
        try Self.expect(streamResponse, body: Data(), codes: [200])

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

    static func expect(_ response: URLResponse, body: Data, codes: Set<Int>) throws {
        guard let http = response as? HTTPURLResponse else { return }
        guard codes.contains(http.statusCode) else {
            let message = (try? JSONDecoder().decode([String: String].self, from: body))?["error"]
                ?? String(decoding: body.prefix(200), as: UTF8.self)
            throw CheckError.badResponse(status: http.statusCode, message: message)
        }
    }

    /// `GET /v1/healthz`, for Settings' "Test connection".
    func health() async -> Result<Void, any Error> {
        do {
            var r = URLRequest(url: baseURL.appending(path: "v1/healthz"))
            r.timeoutInterval = 5
            let (body, response) = try await session.data(for: r)
            try Self.expect(response, body: body, codes: [200])
            return .success(())
        } catch {
            return .failure(error)
        }
    }
}
