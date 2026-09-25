import CryptoKit
import Foundation

/// What the app signs for one hosted-API request, byte for byte what
/// `internal/sparkjudge/auth.go` (`clientData`) rebuilds on the server:
///
///     METHOD + "\n" + PATH?QUERY + "\n" + hex(SHA256(body))
///
/// PATH?QUERY is Go's `r.URL.RequestURI()`: the path and query exactly as sent,
/// percent-encoding included. The assertion is made over SHA256 of this string,
/// so one assertion cannot be moved onto another path or another body.
enum RequestSigning {
    /// SHA-256 of the empty body (`e3b0c442…b855`), what a GET signs.
    static let emptyBodyHash = hex(SHA256.hash(data: Data()))

    static func bodyHash(_ body: Data?) -> String {
        hex(SHA256.hash(data: body ?? Data()))
    }

    /// The path and query as the server sees them in `RequestURI()`.
    static func requestURI(_ url: URL) -> String {
        var path = url.path(percentEncoded: true)
        if path.isEmpty { path = "/" }
        if let query = url.query(percentEncoded: true) { return path + "?" + query }
        return path
    }

    static func clientData(method: String, url: URL, body: Data?) -> Data {
        Data("\(method.uppercased())\n\(requestURI(url))\n\(bodyHash(body))".utf8)
    }

    /// What `generateAssertion` is given: SHA256(clientData).
    static func clientDataHash(method: String, url: URL, body: Data?) -> Data {
        Data(SHA256.hash(data: clientData(method: method, url: url, body: body)))
    }

    static func hex<D: Sequence>(_ digest: D) -> String where D.Element == UInt8 {
        digest.map { String(format: "%02x", $0) }.joined()
    }
}

/// The headers the hosted API reads (`internal/sparkjudge/auth.go`).
enum HostedHeader {
    static let user = "X-App-User-Id"
    static let key = "X-App-Attest-Key"
    static let assertion = "X-App-Attest-Assertion"

    /// Adds the identity to a request: the user id always, the key and the
    /// assertion (base64) when the request is signed.
    static func apply(to request: inout URLRequest, user: String, key: String?, assertion: Data?) {
        request.setValue(user, forHTTPHeaderField: Self.user)
        if let key, let assertion {
            request.setValue(key, forHTTPHeaderField: Self.key)
            request.setValue(assertion.base64EncodedString(), forHTTPHeaderField: Self.assertion)
        } else {
            request.setValue(nil, forHTTPHeaderField: Self.key)
            request.setValue(nil, forHTTPHeaderField: Self.assertion)
        }
    }
}
