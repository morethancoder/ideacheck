import Foundation
import Observation

/// Drives one dictation take: starts a transcriber, smooths the mic level for
/// the orb, ends the take after the silence cutoff, and hands the text over.
@MainActor
@Observable
final class DictationModel {
    enum Phase: Equatable {
        case idle
        case preparing(String)
        case listening
        case finishing
        case failed(String)
    }

    private(set) var phase: Phase = .idle
    private(set) var finalized = ""
    private(set) var volatile = ""
    /// Smoothed mic level in [0, 1], updated every frame while listening.
    private(set) var level: Float = 0
    /// How close the silence cutoff is, 0…1.
    private(set) var silenceProgress: Double = 0
    /// A calm line under the orb when a take ended with nothing said.
    private(set) var hint: String?

    /// Called with the take's text when it ends with words in it.
    var onTake: ((String) -> Void)?
    /// Builds the transcriber for each take; replaced by the demo launch argument.
    var makeTranscriber: () -> any Transcriber = {
        AppSettings.transcriber.make(vadSensitivity: AppSettings.vadSensitivity)
    }

    private var transcriber: (any Transcriber)?
    private var listenTask: Task<Void, Never>?
    private var tickTask: Task<Void, Never>?
    private var gate = SilenceGate(silence: AppSettings.defaultSilence)
    private var smoother = LevelSmoother()
    private var rawLevel: Float = 0
    private var vad: Bool?
    private let clock = ContinuousClock()
    private var origin = ContinuousClock.now

    var text: String { (finalized + volatile).trimmingCharacters(in: .whitespacesAndNewlines) }
    var isActive: Bool {
        switch phase {
        case .preparing, .listening, .finishing: true
        default: false
        }
    }

    func toggle() {
        isActive ? finish() : start()
    }

    func start() {
        guard !isActive else { return }
        finalized = ""
        volatile = ""
        hint = nil
        rawLevel = 0
        vad = nil
        smoother = LevelSmoother()
        gate = SilenceGate(silence: AppSettings.silenceSeconds)
        origin = clock.now
        phase = .preparing("Getting ready…")

        let transcriber = makeTranscriber()
        self.transcriber = transcriber
        let locale = AppSettings.localeIdentifier.map(Locale.init(identifier:)) ?? .current
        listenTask = Task { [weak self] in
            let stream = await transcriber.start(locale: locale)
            do {
                for try await event in stream {
                    self?.handle(event)
                }
                self?.ended(error: nil)
            } catch {
                self?.ended(error: error)
            }
        }
    }

    /// Ends the take now and keeps what was heard.
    func finish() {
        guard isActive, phase != .finishing else { return }
        phase = .finishing
        tickTask?.cancel()
        let t = transcriber
        Task { await t?.stop() }
    }

    func cancel() {
        listenTask?.cancel()
        tickTask?.cancel()
        let t = transcriber
        Task { await t?.cancel() }
        transcriber = nil
        phase = .idle
        level = 0
    }

    private func handle(_ event: TranscriberEvent) {
        switch event {
        case .preparing(let message):
            phase = .preparing(message)
        case .listening:
            if phase != .finishing { phase = .listening }
            startTicking()
        case .level(let l):
            rawLevel = l
        case .transcript(let f, let v):
            finalized = f
            volatile = v
        case .speech(let s):
            vad = s
        }
    }

    /// ~60 Hz: smooth the level for the orb and ask the gate whether to stop.
    private func startTicking() {
        tickTask?.cancel()
        tickTask = Task { [weak self] in
            var last = ContinuousClock.now
            while !Task.isCancelled {
                try? await Task.sleep(for: .milliseconds(16))
                guard let self else { return }
                let now = ContinuousClock.now
                let dt = Double((now - last).components.attoseconds) / 1e18 + Double((now - last).components.seconds)
                last = now
                self.level = self.smoother.next(self.rawLevel, dt: dt)
                let t = self.seconds(since: self.origin, now: now)
                // A voice-activity "no" only counts once the level agrees, so a
                // detector that lags never cuts off a loud speaker.
                let speech: Bool? = self.vad.map { $0 || self.level >= self.gate.threshold }
                let decision = self.gate.feed(level: self.level, speech: speech, at: t)
                self.silenceProgress = self.gate.silenceProgress(at: t)
                switch decision {
                case .keepListening: continue
                case .endTake, .gaveUp:
                    self.finish()
                    return
                }
            }
        }
    }

    private func seconds(since start: ContinuousClock.Instant, now: ContinuousClock.Instant) -> TimeInterval {
        let d = now - start
        return Double(d.components.seconds) + Double(d.components.attoseconds) / 1e18
    }

    private func ended(error: (any Error)?) {
        tickTask?.cancel()
        transcriber = nil
        level = 0
        silenceProgress = 0
        let words = text
        if let error, words.isEmpty {
            phase = .failed(error.localizedDescription)
            return
        }
        phase = .idle
        if words.isEmpty {
            hint = "Didn't catch anything. Tap the orb and say your idea."
        } else {
            onTake?(words)
        }
    }
}
