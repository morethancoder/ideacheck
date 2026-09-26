import Foundation
import Observation
import Speech

/// Whether an engine can be used here, as the device itself reports it.
enum EngineAvailability: Sendable, Equatable {
    case ready
    /// Supported, but its model for the chosen language is not on the device yet.
    case needsDownload
    case unsupported(String)
    case comingSoon

    var isSelectable: Bool { self == .ready || self == .needsDownload }

    var label: String {
        switch self {
        case .ready: "Ready"
        case .needsDownload: "Downloads on first use"
        case .unsupported: "Not on this device"
        case .comingSoon: "Coming soon"
        }
    }
}

struct EngineOption: Identifiable, Sendable, Equatable {
    var engine: TranscriberEngineID
    var availability: EngineAvailability
    var id: TranscriberEngineID { engine }
}

struct LocaleOption: Identifiable, Sendable, Hashable {
    var identifier: String
    var name: String
    var installed: Bool
    var id: String { identifier }
}

/// Builds the Settings picker from what the Speech framework says this device
/// supports: `SpeechTranscriber.isAvailable`, each module's `supportedLocales`
/// and `installedLocales`, and `AssetInventory.status(forModules:)`. Nothing
/// here is a hardcoded list of what works.
@MainActor
@Observable
final class TranscriberCatalog {
    private(set) var options: [EngineOption] = TranscriberEngineID.allCases.map {
        EngineOption(engine: $0, availability: $0.isImplemented ? .unsupported("Checking…") : .comingSoon)
    }
    private(set) var locales: [LocaleOption] = []
    private(set) var loaded = false

    func refresh(engine: TranscriberEngineID, localeIdentifier: String?) async {
        let locale = localeIdentifier.map(Locale.init(identifier:)) ?? .current
        var result: [EngineOption] = []
        for id in TranscriberEngineID.allCases {
            result.append(EngineOption(engine: id, availability: await Self.availability(of: id, locale: locale)))
        }
        options = result
        locales = await Self.locales(for: engine)
        loaded = true
    }

    static func availability(of engine: TranscriberEngineID, locale: Locale) async -> EngineAvailability {
        switch engine {
        case .appleSpeech:
            guard SpeechTranscriber.isAvailable else { return .unsupported("SpeechTranscriber is not available on this device") }
            guard let l = await SpeechTranscriber.supportedLocale(equivalentTo: locale) else { return .unsupported("No model for this language") }
            return await status(SpeechTranscriber(locale: l, preset: .progressiveTranscription))
        case .appleDictation:
            guard let l = await DictationTranscriber.supportedLocale(equivalentTo: locale) else { return .unsupported("No model for this language") }
            return await status(DictationTranscriber(locale: l, preset: .progressiveLongDictation))
        default:
            return .comingSoon
        }
    }

    private static func status(_ module: any SpeechModule) async -> EngineAvailability {
        switch await AssetInventory.status(forModules: [module]) {
        case .installed: .ready
        case .supported, .downloading: .needsDownload
        case .unsupported: .unsupported("Speech assets are not supported here")
        @unknown default: .unsupported("Unknown status")
        }
    }

    static func locales(for engine: TranscriberEngineID) async -> [LocaleOption] {
        let supported: [Locale]
        let installed: [Locale]
        switch engine {
        case .appleSpeech:
            guard SpeechTranscriber.isAvailable else { return [] }
            supported = await SpeechTranscriber.supportedLocales
            installed = await SpeechTranscriber.installedLocales
        case .appleDictation:
            supported = await DictationTranscriber.supportedLocales
            installed = await DictationTranscriber.installedLocales
        default:
            return []
        }
        let installedIDs = Set(installed.map(\.identifier))
        return supported
            .map { LocaleOption(identifier: $0.identifier,
                                name: Locale.current.localizedString(forIdentifier: $0.identifier) ?? $0.identifier,
                                installed: installedIDs.contains($0.identifier)) }
            .sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
    }
}
