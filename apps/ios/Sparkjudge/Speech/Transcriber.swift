import Foundation
import Speech

/// What a transcriber reports while it listens.
enum TranscriberEvent: Sendable, Equatable {
    /// Setting up before audio flows (e.g. "Downloading the English model…").
    case preparing(String)
    /// Audio is flowing; words may follow.
    case listening
    /// Microphone loudness for one buffer, normalized to [0, 1].
    case level(Float)
    /// The whole transcript so far: settled text plus the current guess.
    case transcript(finalized: String, volatile: String)
    /// Voice activity from the engine's own detector, when it has one.
    case speech(Bool)
}

enum TranscriberError: LocalizedError, Equatable {
    case microphoneDenied
    case noMicrophone
    case unsupportedDevice(String)
    case unsupportedLocale(String)
    case notImplemented(String)
    case audio(String)

    var errorDescription: String? {
        switch self {
        case .microphoneDenied: "Sparkjudge can't hear you: microphone access is off. Turn it on in Settings › Privacy › Microphone, or type the idea instead."
        case .noMicrophone: "No microphone is available right now. You can type the idea instead."
        case .unsupportedDevice(let engine): "\(engine) can't run on this device. Pick another engine in Settings, or type the idea instead."
        case .unsupportedLocale(let locale): "The chosen engine has no model for \(locale). Pick another language in Settings."
        case .notImplemented(let engine): "\(engine) is coming soon. Pick an Apple engine in Settings for now."
        case .audio(let why): "The microphone could not start: \(why)"
        }
    }
}

/// A speech-to-text engine. `start` returns at once; setup, audio and results
/// all arrive on the stream, which finishes after `stop()` once the last words
/// are settled, or throws if the engine cannot run.
protocol Transcriber: AnyObject, Sendable {
    func start(locale: Locale) async -> AsyncThrowingStream<TranscriberEvent, any Error>
    /// Stops listening and settles what was heard; the stream then finishes.
    func stop() async
    /// Stops at once, dropping any unsettled words.
    func cancel() async
}

/// Every engine the picker can list. Only the Apple ones are implemented; the
/// others are the plug-in points for later (WhisperKit sizes, Parakeet).
enum TranscriberEngineID: String, CaseIterable, Codable, Sendable, Identifiable {
    case appleSpeech = "apple.speech"
    case appleDictation = "apple.dictation"
    case whisperTiny = "whisperkit.tiny"
    case whisperBase = "whisperkit.base"
    case whisperSmall = "whisperkit.small"
    case whisperTurbo = "whisperkit.large-v3-turbo"
    case parakeet = "parakeet.tdt-0.6b"

    var id: String { rawValue }

    var name: String {
        switch self {
        case .appleSpeech: "Apple Speech"
        case .appleDictation: "Apple Dictation"
        case .whisperTiny: "Whisper Tiny"
        case .whisperBase: "Whisper Base"
        case .whisperSmall: "Whisper Small"
        case .whisperTurbo: "Whisper Large v3 Turbo"
        case .parakeet: "Parakeet TDT 0.6B"
        }
    }

    var detail: String {
        switch self {
        case .appleSpeech: "SpeechAnalyzer + SpeechTranscriber. On-device, fast, long-form."
        case .appleDictation: "SpeechAnalyzer + DictationTranscriber. The keyboard's dictation model; runs on more devices."
        case .whisperTiny: "WhisperKit, 39M. Fastest Whisper, rough on names."
        case .whisperBase: "WhisperKit, 74M. A good balance on older phones."
        case .whisperSmall: "WhisperKit, 244M. Clearly better, needs an A15 or newer."
        case .whisperTurbo: "WhisperKit, 809M. Best Whisper quality; A17 Pro / M-series."
        case .parakeet: "NVIDIA Parakeet via Core ML. Very fast English."
        }
    }

    /// 1–3, for the picker's speed and quality marks.
    var speed: Int {
        switch self {
        case .appleSpeech, .whisperTiny, .parakeet: 3
        case .appleDictation, .whisperBase: 2
        case .whisperSmall, .whisperTurbo: 1
        }
    }

    var quality: Int {
        switch self {
        case .whisperTiny: 1
        case .appleDictation, .whisperBase: 2
        case .appleSpeech, .whisperSmall, .whisperTurbo, .parakeet: 3
        }
    }

    var hardware: String {
        switch self {
        case .appleSpeech: "Checked on this device"
        case .appleDictation: "Most iOS 26 devices"
        case .whisperTiny, .whisperBase: "Any iOS 26 device"
        case .whisperSmall: "A15 or newer"
        case .whisperTurbo: "A17 Pro or newer"
        case .parakeet: "A16 or newer"
        }
    }

    var isImplemented: Bool { self == .appleSpeech || self == .appleDictation }

    @MainActor
    func make(vadSensitivity: Int) -> any Transcriber {
        switch self {
        // SpeechTranscriber needs newer hardware; where the device says it can't
        // run, the dictation model (same analyzer, broader hardware) stands in.
        case .appleSpeech where !SpeechTranscriber.isAvailable:
            AppleSpeechTranscriber(model: .dictation, vadSensitivity: vadSensitivity)
        case .appleSpeech: AppleSpeechTranscriber(model: .speech, vadSensitivity: vadSensitivity)
        case .appleDictation: AppleSpeechTranscriber(model: .dictation, vadSensitivity: vadSensitivity)
        default: UnavailableTranscriber(error: .notImplemented(name))
        }
    }
}

/// Stands in for an engine that is listed but not built yet.
final class UnavailableTranscriber: Transcriber {
    let error: TranscriberError
    init(error: TranscriberError) { self.error = error }

    func start(locale: Locale) async -> AsyncThrowingStream<TranscriberEvent, any Error> {
        let error = self.error
        return AsyncThrowingStream { $0.finish(throwing: error) }
    }
    func stop() async {}
    func cancel() async {}
}
