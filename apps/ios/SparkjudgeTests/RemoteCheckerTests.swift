import Foundation
import Testing
@testable import Sparkjudge

/// Serves canned responses by path, delivering bodies in small chunks so the
/// SSE reader sees events split across reads.
final class StubProtocol: URLProtocol, @unchecked Sendable {
    struct Route: Sendable {
        var status: Int
        var body: Data
        var contentType = "application/json"
    }

    private static let lock = NSLock()
    nonisolated(unsafe) private static var routes: [String: Route] = [:]
    nonisolated(unsafe) private static var seen: [(URLRequest, Data)] = []

    static func install(_ r: [String: Route]) {
        lock.withLock { routes = r; seen = [] }
    }

    static var requests: [(URLRequest, Data)] { lock.withLock { seen } }

    static func session() -> URLSession {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [StubProtocol.self]
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
        let route = Self.lock.withLock { () -> Route? in
            Self.seen.append((request, body))
            return Self.routes[request.url?.path ?? ""]
        } ?? Route(status: 404, body: Data(#"{"status":"error","error":"no route"}"#.utf8))
        let response = HTTPURLResponse(url: request.url!, statusCode: route.status, httpVersion: "HTTP/1.1",
                                       headerFields: ["Content-Type": route.contentType])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        var i = route.body.startIndex
        while i < route.body.endIndex {
            let end = route.body.index(i, offsetBy: 37, limitedBy: route.body.endIndex) ?? route.body.endIndex
            client?.urlProtocol(self, didLoad: route.body[i..<end])
            i = end
        }
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

@Suite(.serialized)
struct RemoteCheckerTests {
    let base = URL(string: "http://127.0.0.1:8080")!

    private func accepted(_ id: String = "chk_1") -> StubProtocol.Route {
        .init(status: 202, body: Data(#"{"id":"\#(id)","events":"/v1/checks/\#(id)/events"}"#.utf8))
    }

    /// The server's own framing: `event: <name>\ndata: <json>\n\n`. The result is
    /// pretty-printed JSON split over several `data:` lines, which SSE joins with newlines.
    private func sse(result: Data) -> Data {
        let progress = #"{"type":"started","stage":"preflight","question":{"id":"has_problem","kind":"noul","instructions":"Does it state a problem?"}}"#
        let answered = #"{"type":"answered","stage":"score","question":{"id":"clarity","kind":"score","weight":0.5,"polarity":1},"value":0.2}"#
        var s = ": keep-alive\n\n"
        s += "event: progress\ndata: \(progress)\n\n"
        s += "event: progress\r\ndata: \(answered)\r\n\r\n"
        s += "event: result\n"
        s += String(decoding: result, as: UTF8.self).split(separator: "\n", omittingEmptySubsequences: false).map { "data: \($0)" }.joined(separator: "\n")
        s += "\n\n"
        return Data(s.utf8)
    }

    private func collect(_ checker: RemoteChecker, _ intake: Intake, rubric: String? = nil) async throws -> [CheckEvent] {
        var events: [CheckEvent] = []
        for try await e in checker.check(intake, rubric: rubric) { events.append(e) }
        return events
    }

    @Test func postsTheIntakeThenStreamsProgressAndResult() async throws {
        let fixture = try Fixture.mockResult()
        StubProtocol.install([
            "/v1/check": accepted(),
            "/v1/checks/chk_1/events": .init(status: 200, body: sse(result: fixture), contentType: "text/event-stream"),
        ])
        let checker = RemoteChecker(baseURL: base, session: StubProtocol.session())
        let intake = Intake(idea: "a to-do app for dentists", fields: ["title": "Chairside"])
        let events = try await collect(checker, intake, rubric: "side_project")

        guard case .accepted(let id) = events.first else { Issue.record("first event is not accepted"); return }
        #expect(id == "chk_1")
        let progress = events.compactMap { if case .progress(let p) = $0 { p } else { nil } }
        #expect(progress.map(\.question.id) == ["has_problem", "clarity"])
        #expect(progress[1].value == 0.2 && progress[1].isFinished)
        guard case .result(let result, let raw) = events.last else { Issue.record("last event is not a result"); return }
        #expect(result.id == "chk_01M3CX7MXGFB6QVCFTHY7MDTQH")
        #expect(try CheckResult.decode(raw) == result)

        let (post, body) = try #require(StubProtocol.requests.first)
        #expect(post.httpMethod == "POST")
        let query = URLComponents(url: post.url!, resolvingAgainstBaseURL: false)?.queryItems ?? []
        #expect(query.contains(URLQueryItem(name: "async", value: "1")))
        #expect(query.contains(URLQueryItem(name: "rubric", value: "side_project")))
        #expect(try JSONDecoder().decode(Intake.self, from: body) == intake)
    }

    @Test func anErrorEventThrowsItsMessage() async throws {
        StubProtocol.install([
            "/v1/check": accepted("chk_2"),
            "/v1/checks/chk_2/events": .init(status: 200, body: Data("event: error\ndata: {\"error\":\"judge unreachable\"}\n\n".utf8), contentType: "text/event-stream"),
        ])
        let checker = RemoteChecker(baseURL: base, session: StubProtocol.session())
        await #expect(throws: CheckError.server("judge unreachable")) {
            _ = try await collect(checker, Intake(idea: "x"))
        }
    }

    @Test func aRejectedPostSurfacesTheServersError() async throws {
        StubProtocol.install(["/v1/check": .init(status: 400, body: Data(#"{"status":"error","error":"intake JSON: unknown field"}"#.utf8))])
        let checker = RemoteChecker(baseURL: base, session: StubProtocol.session())
        await #expect(throws: CheckError.badResponse(status: 400, message: "intake JSON: unknown field")) {
            _ = try await collect(checker, Intake(idea: "x"))
        }
    }

    @Test func aStreamThatStopsWithoutAResultIsAnError() async throws {
        StubProtocol.install([
            "/v1/check": accepted("chk_3"),
            "/v1/checks/chk_3/events": .init(status: 200, body: Data("event: progress\ndata: {}\n\n".utf8), contentType: "text/event-stream"),
        ])
        let checker = RemoteChecker(baseURL: base, session: StubProtocol.session())
        await #expect(throws: CheckError.streamEnded) {
            _ = try await collect(checker, Intake(idea: "x"))
        }
    }

    @Test func intakeEncodesAsTheEngineExpects() throws {
        let json = try #require(try JSONSerialization.jsonObject(with: JSONEncoder().encode(Intake(idea: "i", fields: ["why_now": "w"]))) as? [String: Any])
        #expect(Set(json.keys) == ["idea", "fields"]) // nil context/profile are omitted, not null
    }
}

struct SSEParserTests {
    @Test func parsesEventsDataAndComments() {
        var p = SSEParser()
        let lines = [": hello", "event: progress", "data: {\"a\":1}", "", "data: plain", "data: two", "", ""]
        let messages = lines.compactMap { p.feed(line: $0) }
        #expect(messages == [SSEMessage(event: "progress", data: "{\"a\":1}"), SSEMessage(event: "message", data: "plain\ntwo")])
    }

    @Test func handlesCRLFAndNoSpaceAfterColon() {
        var p = SSEParser()
        #expect(p.feed(line: "event:result\r") == nil)
        #expect(p.feed(line: "data:{}\r") == nil)
        #expect(p.feed(line: "\r") == SSEMessage(event: "result", data: "{}"))
    }

    @Test func finishFlushesATrailingMessage() {
        var p = SSEParser()
        _ = p.feed(line: "event: result")
        _ = p.feed(line: "data: x")
        #expect(p.finish() == SSEMessage(event: "result", data: "x"))
        #expect(p.finish() == nil)
    }

    @Test func lineSplitterKeepsEmptyLines() {
        var s = LineSplitter()
        let lines = Array("a\n\nb\nc".utf8).compactMap { s.push($0) }
        #expect(lines == ["a", "", "b"])
        #expect(s.rest() == "c")
    }
}
