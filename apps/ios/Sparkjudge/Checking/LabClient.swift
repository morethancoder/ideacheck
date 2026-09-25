import Foundation

/// The hosted API's Lab routes (docs/sparkjudge-api.md, "The Lab"):
/// `POST /v1/mix` and `GET /v1/trends`. Both are signed like every hosted
/// request; `sign` is where App Attest's assertion goes once the app has one.
struct LabClient: Sendable {
    let baseURL: URL
    var session: URLSession = .shared
    /// The app's user id, the same one RevenueCat knows (`X-App-User-Id`).
    var userID: String = LabIdentity.userID
    /// Adds whatever proves the request came from this app (App Attest's
    /// assertion headers). Identity by default: fine for a server running with
    /// `-attest off`, as `make api` does.
    var sign: @Sendable (URLRequest, Data) async throws -> URLRequest = { request, _ in request }

    struct ServerError: LocalizedError, Equatable {
        var status: Int
        var code: String
        var message: String

        var errorDescription: String? { message }
    }

    func mix(_ inputs: [MixInput]) async throws -> MixResponse {
        let body = try JSONEncoder().encode(MixRequest(inputs))
        let data = try await send("POST", path: "v1/mix", body: body, timeout: 90)
        return try JSONDecoder().decode(MixResponse.self, from: data)
    }

    /// The report and its JSON as received, which the app keeps for offline use.
    func trends() async throws -> (TrendsReport, Data) {
        let data = try await send("GET", path: "v1/trends", body: Data(), timeout: 120)
        return (try TrendsReport.decode(data), data)
    }

    func request(_ method: String, path: String, body: Data) -> URLRequest {
        var r = URLRequest(url: baseURL.appending(path: path))
        r.httpMethod = method
        r.setValue(userID, forHTTPHeaderField: "X-App-User-Id")
        r.setValue("application/json", forHTTPHeaderField: "Accept")
        if method == "POST" {
            r.setValue("application/json", forHTTPHeaderField: "Content-Type")
            r.httpBody = body
        }
        return r
    }

    private func send(_ method: String, path: String, body: Data, timeout: TimeInterval) async throws -> Data {
        var r = try await sign(request(method, path: path, body: body), body)
        r.timeoutInterval = timeout
        let (data, response) = try await session.data(for: r)
        try Self.expect(response, body: data)
        return data
    }

    /// Turns the API's error body (`{"error": code, "message": …}`) into an error
    /// whose description is the server's own sentence.
    static func expect(_ response: URLResponse, body: Data) throws {
        guard let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) else { return }
        let decoded = try? JSONDecoder().decode([String: String].self, from: body)
        throw ServerError(status: http.statusCode, code: decoded?["error"] ?? "http_\(http.statusCode)",
                          message: decoded?["message"] ?? "The server answered \(http.statusCode).")
    }
}

/// The app's user id for the hosted API: a UUID made once. The paid tier keeps
/// its own in the Keychain; until the two meet this one stands in.
enum LabIdentity {
    static let key = "labUserID"

    static var userID: String {
        let d = UserDefaults.standard
        if let id = d.string(forKey: key), UUID(uuidString: id) != nil { return id }
        let id = UUID().uuidString.lowercased()
        d.set(id, forKey: key)
        return id
    }
}
