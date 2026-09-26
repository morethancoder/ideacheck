import AVFAudio
import Foundation
import Speech

/// On-device transcription with iOS 26's SpeechAnalyzer: a SpeechTranscriber (or
/// DictationTranscriber, which runs on more hardware) for the words, and a
/// SpeechDetector for voice activity, fed from an AVAudioEngine microphone tap.
actor AppleSpeechTranscriber: Transcriber {
    enum Model: Sendable { case speech, dictation }

    let model: Model
    let vadSensitivity: SpeechDetector.SensitivityLevel

    private var engine: AVAudioEngine?
    private var analyzer: SpeechAnalyzer?
    private var input: AsyncStream<AnalyzerInput>.Continuation?
    private var output: AsyncThrowingStream<TranscriberEvent, any Error>.Continuation?
    private var resultTasks: [Task<Void, Never>] = []
    private var setup: Task<Void, Never>?

    init(model: Model, vadSensitivity: Int) {
        self.model = model
        self.vadSensitivity = SpeechDetector.SensitivityLevel(rawValue: vadSensitivity) ?? .medium
    }

    var engineName: String { model == .speech ? "Apple Speech" : "Apple Dictation" }

    func start(locale: Locale) async -> AsyncThrowingStream<TranscriberEvent, any Error> {
        let (stream, continuation) = AsyncThrowingStream.makeStream(of: TranscriberEvent.self, throwing: (any Error).self)
        output = continuation
        setup = Task {
            do {
                try await self.prepareAndRun(locale: locale, continuation: continuation)
            } catch {
                await self.tearDown()
                continuation.finish(throwing: error)
            }
        }
        return stream
    }

    func stop() async {
        await setup?.value
        engine?.stop()
        engine?.inputNode.removeTap(onBus: 0)
        input?.finish()
        input = nil
        if let analyzer {
            try? await analyzer.finalizeAndFinishThroughEndOfInput()
        }
        for task in resultTasks { await task.value }
        await tearDown()
        output?.finish()
        output = nil
    }

    func cancel() async {
        setup?.cancel()
        engine?.stop()
        engine?.inputNode.removeTap(onBus: 0)
        input?.finish()
        await analyzer?.cancelAndFinishNow()
        resultTasks.forEach { $0.cancel() }
        await tearDown()
        output?.finish()
        output = nil
    }

    private func tearDown() async {
        engine = nil
        analyzer = nil
        input = nil
        resultTasks = []
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    // MARK: - Setup

    private func prepareAndRun(locale: Locale, continuation: AsyncThrowingStream<TranscriberEvent, any Error>.Continuation) async throws {
        try await Self.requireMicrophone()

        // The transcriber module, for a locale the engine actually supports.
        let words: any SpeechModule
        let supported: Locale
        switch model {
        case .speech:
            guard SpeechTranscriber.isAvailable else { throw TranscriberError.unsupportedDevice(engineName) }
            guard let l = await SpeechTranscriber.supportedLocale(equivalentTo: locale) else {
                throw TranscriberError.unsupportedLocale(locale.localizedString(forIdentifier: locale.identifier) ?? locale.identifier)
            }
            supported = l
            words = SpeechTranscriber(locale: l, transcriptionOptions: [], reportingOptions: [.volatileResults, .fastResults], attributeOptions: [])
        case .dictation:
            guard let l = await DictationTranscriber.supportedLocale(equivalentTo: locale) else {
                throw TranscriberError.unsupportedLocale(locale.localizedString(forIdentifier: locale.identifier) ?? locale.identifier)
            }
            supported = l
            words = DictationTranscriber(locale: l, preset: .progressiveLongDictation)
        }
        let detector = SpeechDetector(detectionOptions: .init(sensitivityLevel: vadSensitivity), reportResults: true)
        let modules: [any SpeechModule] = [words, detector]

        // Models are downloaded on demand; say so rather than sit silent.
        if let request = try await AssetInventory.assetInstallationRequest(supporting: modules) {
            let name = supported.localizedString(forIdentifier: supported.identifier) ?? supported.identifier
            continuation.yield(.preparing("Downloading the \(name) speech model…"))
            try await request.downloadAndInstall()
        }
        try Task.checkCancellation()

        guard let analyzerFormat = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: modules) else {
            throw TranscriberError.audio("no audio format suits the speech model")
        }
        let analyzer = SpeechAnalyzer(modules: modules, options: .init(priority: .userInitiated, modelRetention: .lingering))
        try await analyzer.prepareToAnalyze(in: analyzerFormat)
        self.analyzer = analyzer

        // Results: words and voice activity, each on its own task.
        resultTasks.append(Task { await Self.forward(words: words, to: continuation) })
        resultTasks.append(Task {
            do {
                for try await r in detector.results { continuation.yield(.speech(r.speechDetected)) }
            } catch {}
        })

        // Microphone → converter → analyzer.
        let (inputStream, inputContinuation) = AsyncStream.makeStream(of: AnalyzerInput.self)
        self.input = inputContinuation
        let engine = try Self.startMicrophone(analyzerFormat: analyzerFormat, input: inputContinuation, events: continuation)
        self.engine = engine
        try await analyzer.start(inputSequence: inputStream)
        continuation.yield(.listening)
    }

    private static func forward(words: any SpeechModule, to continuation: AsyncThrowingStream<TranscriberEvent, any Error>.Continuation) async {
        var finalized = ""
        func apply(_ text: AttributedString, isFinal: Bool) {
            let s = String(text.characters)
            if isFinal {
                finalized += s
                continuation.yield(.transcript(finalized: finalized, volatile: ""))
            } else {
                continuation.yield(.transcript(finalized: finalized, volatile: s))
            }
        }
        do {
            if let t = words as? SpeechTranscriber {
                for try await r in t.results { apply(r.text, isFinal: r.isFinal) }
            } else if let t = words as? DictationTranscriber {
                for try await r in t.results { apply(r.text, isFinal: r.isFinal) }
            }
        } catch {
            continuation.finish(throwing: error)
        }
    }

    private static func requireMicrophone() async throws {
        switch AVAudioApplication.shared.recordPermission {
        case .granted: return
        case .denied: throw TranscriberError.microphoneDenied
        default:
            guard await AVAudioApplication.requestRecordPermission() else { throw TranscriberError.microphoneDenied }
        }
    }

    private static func startMicrophone(analyzerFormat: AVAudioFormat,
                                        input: AsyncStream<AnalyzerInput>.Continuation,
                                        events: AsyncThrowingStream<TranscriberEvent, any Error>.Continuation) throws -> AVAudioEngine {
        let session = AVAudioSession.sharedInstance()
        do {
            try session.setCategory(.record, mode: .measurement, options: [.duckOthers])
            try session.setActive(true, options: .notifyOthersOnDeactivation)
        } catch {
            throw TranscriberError.audio(error.localizedDescription)
        }
        guard session.isInputAvailable else { throw TranscriberError.noMicrophone }

        let engine = AVAudioEngine()
        let node = engine.inputNode
        let micFormat = node.outputFormat(forBus: 0)
        guard micFormat.sampleRate > 0, micFormat.channelCount > 0 else { throw TranscriberError.noMicrophone }
        let pump = AudioPump(from: micFormat, to: analyzerFormat, input: input, events: events)
        node.installTap(onBus: 0, bufferSize: 1024, format: micFormat, block: pump.tapBlock())
        engine.prepare()
        do {
            try engine.start()
        } catch {
            node.removeTap(onBus: 0)
            throw TranscriberError.audio(error.localizedDescription)
        }
        return engine
    }
}

/// Runs on the audio thread: measures each buffer's level and converts it to
/// the analyzer's format. Deliberately not actor-isolated — the tap block is
/// called on a realtime thread, and an isolated closure would trap there.
final class AudioPump: @unchecked Sendable {
    private let converter: AVAudioConverter?
    private let target: AVAudioFormat
    private let input: AsyncStream<AnalyzerInput>.Continuation
    private let events: AsyncThrowingStream<TranscriberEvent, any Error>.Continuation

    init(from source: AVAudioFormat, to target: AVAudioFormat,
         input: AsyncStream<AnalyzerInput>.Continuation,
         events: AsyncThrowingStream<TranscriberEvent, any Error>.Continuation) {
        self.target = target
        self.input = input
        self.events = events
        if source == target {
            converter = nil
        } else {
            let c = AVAudioConverter(from: source, to: target)
            c?.primeMethod = .none
            converter = c
        }
    }

    nonisolated func tapBlock() -> AVAudioNodeTapBlock {
        { [self] buffer, _ in self.process(buffer) }
    }

    nonisolated private func process(_ buffer: AVAudioPCMBuffer) {
        events.yield(.level(LevelMeter.level(of: buffer)))
        guard let converter else {
            input.yield(AnalyzerInput(buffer: buffer))
            return
        }
        let ratio = target.sampleRate / buffer.format.sampleRate
        let capacity = AVAudioFrameCount((Double(buffer.frameLength) * ratio).rounded(.up)) + 16
        guard let out = AVAudioPCMBuffer(pcmFormat: target, frameCapacity: capacity) else { return }
        let feed = OneShot(buffer)
        var error: NSError?
        let status = converter.convert(to: out, error: &error) { _, outStatus in
            if let b = feed.take() {
                outStatus.pointee = .haveData
                return b
            }
            outStatus.pointee = .noDataNow
            return nil
        }
        if status != .error, out.frameLength > 0 {
            input.yield(AnalyzerInput(buffer: out))
        }
    }
}

/// Hands a buffer to the converter exactly once.
private final class OneShot: @unchecked Sendable {
    private var buffer: AVAudioPCMBuffer?
    init(_ buffer: AVAudioPCMBuffer) { self.buffer = buffer }
    func take() -> AVAudioPCMBuffer? {
        defer { buffer = nil }
        return buffer
    }
}
