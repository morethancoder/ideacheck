import Foundation

/// The hosted API's error body: `{"status":"error","error":code,"message":…}`.
struct APIErrorBody: Decodable, Sendable, Equatable {
    var error: String?
    var message: String?
}

/// What went wrong with a hosted request, in words a person can act on. The
/// cases follow the status/code table in docs/sparkjudge-api.md.
enum HostedError: LocalizedError, Equatable, Sendable {
    /// 402 `quota_exceeded`. `upgrade` is true when Pro would help (a free
    /// plan); a Pro plan's message says when checks come back instead.
    case quotaExceeded(upgrade: Bool, message: String)
    /// 429 `rate_limited`.
    case rateLimited(String)
    /// 503 `daily_cap`: the whole service spent today's budget.
    case dailyCap
    /// 401 that re-attesting and re-signing did not fix.
    case unauthorized(code: String)
    /// This device cannot attest (the simulator, or an unsupported device)
    /// and the build is not allowed to skip it.
    case attestUnavailable
    /// Anything else the server refused.
    case http(status: Int, code: String, message: String)

    static func from(status: Int, body: Data) -> HostedError {
        let decoded = try? JSONDecoder().decode(APIErrorBody.self, from: body)
        let code = decoded?.error ?? ""
        let message = decoded?.message ?? decoded?.error ?? String(decoding: body.prefix(200), as: UTF8.self)
        switch (status, code) {
        case (402, _):
            return .quotaExceeded(upgrade: message.localizedCaseInsensitiveContains("upgrade"), message: message)
        case (429, _):
            return .rateLimited(message)
        case (503, "daily_cap"):
            return .dailyCap
        case (401, _):
            return .unauthorized(code: code.isEmpty ? "unauthorized" : code)
        default:
            return .http(status: status, code: code, message: message)
        }
    }

    /// The 401 codes that mean "this key is not (or no longer) good here":
    /// attest a new one and try again.
    static let reattestCodes: Set<String> = ["unknown_key", "attestation_required", "bad_assertion"]

    var errorDescription: String? {
        switch self {
        case .quotaExceeded(let upgrade, let message):
            if upgrade { return "You've used this month's free hosted checks. Pro gives you 100 a month, with web research." }
            // "no hosted checks left this month; they come back on October 1"
            let tail = message.split(separator: ";", maxSplits: 1).dropFirst().first
                .map { $0.trimmingCharacters(in: .whitespaces) } ?? ""
            return "You've used this month's hosted checks." + (tail.isEmpty ? "" : " " + tail.prefix(1).uppercased() + tail.dropFirst() + ".")
        case .rateLimited:
            return "That's a lot of checks at once. Give it a few minutes and try again."
        case .dailyCap:
            return "Sparkjudge Cloud is paused for today and picks up again at midnight UTC. This check wasn't counted."
        case .unauthorized(let code):
            return "Sparkjudge Cloud couldn't confirm this copy of the app (\(code)). Try again in a moment."
        case .attestUnavailable:
            return "This device can't prove the app is genuine (App Attest), which Sparkjudge Cloud needs."
        case .http(let status, _, let message):
            return "Sparkjudge Cloud answered \(status): \(message)"
        }
    }

    /// True when showing the paywall is the helpful next step.
    var suggestsUpgrade: Bool {
        if case .quotaExceeded(true, _) = self { return true }
        return false
    }
}
