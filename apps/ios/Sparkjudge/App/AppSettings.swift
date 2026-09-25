import Foundation

/// UserDefaults keys. Views bind them with `@AppStorage`; non-view code reads
/// them through `AppSettings`, so there is one name and one default per setting.
enum SettingsKey {
    static let transcriber = "transcriber"
    static let locale = "transcriptionLocale"
    static let silenceSeconds = "silenceSeconds"
    static let vadSensitivity = "vadSensitivity"
    static let autoCheck = "autoCheck"
    static let checker = "checker"
    /// A server other than the build's own hosted API, for development (Debug only).
    static let serverURL = "serverURL"
    static let proPreview = "proPreview"
    static let ideasLayout = "ideasLayout"
}

enum AppSettings {
    static let defaultSilence: Double = 2.0
    static let silenceRange: ClosedRange<Double> = 0.8...6.0

    static var defaults: UserDefaults { .standard }

    static var silenceSeconds: Double {
        let v = defaults.double(forKey: SettingsKey.silenceSeconds)
        return v == 0 ? defaultSilence : min(max(v, silenceRange.lowerBound), silenceRange.upperBound)
    }

    static var transcriber: TranscriberEngineID {
        defaults.string(forKey: SettingsKey.transcriber).flatMap(TranscriberEngineID.init(rawValue:)) ?? .appleSpeech
    }

    static var localeIdentifier: String? { defaults.string(forKey: SettingsKey.locale) }

    static var vadSensitivity: Int {
        defaults.object(forKey: SettingsKey.vadSensitivity) == nil ? 1 : defaults.integer(forKey: SettingsKey.vadSensitivity)
    }

    static var autoCheck: Bool { defaults.bool(forKey: SettingsKey.autoCheck) }

    /// Who checks: Sparkjudge Cloud (`.remote`) unless chosen otherwise.
    static var checker: CheckerKind {
        defaults.string(forKey: SettingsKey.checker).flatMap(CheckerKind.init(rawValue:)) ?? .remote
    }

    /// The build's hosted API, from Info.plist's `SparkjudgeAPIURL` (the
    /// `SPARKJUDGE_API_URL` build setting in project.yml): `make api` on this
    /// Mac for Debug, the hosted service for Release.
    static let hostedURL: URL = {
        let raw = (Bundle.main.object(forInfoDictionaryKey: "SparkjudgeAPIURL") as? String) ?? ""
        return URL(string: raw).flatMap { $0.scheme == nil ? nil : $0 } ?? URL(string: "https://sparkjudge-api.fly.dev")!
    }()

    /// Where checks go. A Debug build may point anywhere (Settings → Developer),
    /// e.g. at `ideacheck serve`; a Release build always uses its hosted API.
    static var serverURL: URL {
        #if DEBUG
        if let raw = defaults.string(forKey: SettingsKey.serverURL)?.trimmingCharacters(in: .whitespaces),
           !raw.isEmpty, let url = URL(string: raw), url.scheme != nil {
            return url
        }
        #endif
        return hostedURL
    }

    /// RevenueCat's public SDK key from Info.plist (`REVENUECAT_API_KEY`, set in
    /// Config/Secrets.xcconfig); nil when this build has none.
    static var revenueCatAPIKey: String? {
        let key = (Bundle.main.object(forInfoDictionaryKey: "RevenueCatAPIKey") as? String)?
            .trimmingCharacters(in: .whitespaces) ?? ""
        return key.isEmpty || key.hasPrefix("$(") ? nil : key
    }

    static func makeChecker(_ kind: CheckerKind = checker) -> any IdeaChecker {
        switch kind {
        case .remote: RemoteChecker(api: .shared(baseURL: serverURL))
        case .preview: PreviewChecker()
        case .onDevice: OnDeviceChecker()
        }
    }
}
