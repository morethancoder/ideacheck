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
    static let serverURL = "serverURL"
    static let proPreview = "proPreview"
    static let ideasLayout = "ideasLayout"
}

enum AppSettings {
    static let defaultServerURL = "http://127.0.0.1:8080"
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

    static var checker: CheckerKind {
        defaults.string(forKey: SettingsKey.checker).flatMap(CheckerKind.init(rawValue:)) ?? .remote
    }

    static var serverURL: URL {
        let raw = defaults.string(forKey: SettingsKey.serverURL) ?? defaultServerURL
        return URL(string: raw.trimmingCharacters(in: .whitespaces)) ?? URL(string: defaultServerURL)!
    }

    static func makeChecker(_ kind: CheckerKind = checker) -> any IdeaChecker {
        switch kind {
        case .remote: RemoteChecker(baseURL: serverURL)
        case .preview: PreviewChecker()
        case .onDevice: OnDeviceChecker()
        }
    }
}
