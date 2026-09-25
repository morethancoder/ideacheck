import Foundation
import Observation

/// The Trends screen's data: the last report kept on disk, shown at once, then
/// refreshed from `GET /v1/trends`. Offline, the kept one stays with the time
/// it was fetched. In preview mode it is the bundled `SampleTrends.json`.
@MainActor
@Observable
final class TrendsStore {
    private(set) var report: TrendsReport?
    private(set) var loading = false
    /// Why the shown report is not a fresh one, if it is not.
    private(set) var note: String?

    private let fetch: @Sendable () async throws -> (TrendsReport, Data)
    private let cacheURL: URL?

    init(fetch: @escaping @Sendable () async throws -> (TrendsReport, Data), cacheURL: URL? = TrendsStore.defaultCacheURL) {
        self.fetch = fetch
        self.cacheURL = cacheURL
        if let cacheURL, let data = try? Data(contentsOf: cacheURL) {
            report = try? TrendsReport.decode(data)
        }
    }

    static var defaultCacheURL: URL { URL.cachesDirectory.appending(path: "trends.json") }

    /// The store the app uses: the hosted API, or the bundled sample in preview mode.
    static func live(checker: CheckerKind = AppSettings.checker) -> TrendsStore {
        if checker == .preview {
            return TrendsStore(fetch: {
                guard let url = Bundle.main.url(forResource: "SampleTrends", withExtension: "json") else { throw CheckError.unavailable("No sample trends in this build.") }
                let data = try Data(contentsOf: url)
                return (try TrendsReport.decode(data), data)
            }, cacheURL: nil)
        }
        let client = LabClient(baseURL: AppSettings.serverURL)
        return TrendsStore(fetch: { try await client.trends() })
    }

    func refresh() async {
        guard !loading else { return }
        loading = true
        defer { loading = false }
        do {
            let (fresh, data) = try await fetch()
            report = fresh
            note = fresh.stale == true ? "The server couldn't reach its sources today, so these are the last ones it had." : nil
            if let cacheURL { try? data.write(to: cacheURL, options: .atomic) }
        } catch {
            note = report == nil
                ? "Couldn't load trends: \(error.localizedDescription)"
                : "Offline: showing the trends you last loaded."
        }
    }
}
