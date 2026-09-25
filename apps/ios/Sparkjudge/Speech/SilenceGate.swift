import Foundation

/// Decides when a take is over: after the person has spoken, `silence` seconds
/// with no speech end it. Before any speech, `patience` seconds give up. Pure —
/// it only sees levels, voice-activity flags and timestamps.
///
/// Speech is either an engine's voice-activity flag (when one is given it wins)
/// or a level at or above `threshold`.
struct SilenceGate: Sendable, Equatable {
    enum Decision: Sendable, Equatable {
        case keepListening
        /// Speech was heard and then `silence` seconds passed without any.
        case endTake
        /// Nothing was said within `patience` seconds.
        case gaveUp
    }

    var silence: TimeInterval
    var threshold: Float = 0.3
    var patience: TimeInterval = 12

    private(set) var startedAt: TimeInterval?
    private(set) var heardSpeech = false
    private(set) var lastSpeechAt: TimeInterval?

    init(silence: TimeInterval, threshold: Float = 0.3, patience: TimeInterval = 12) {
        self.silence = silence
        self.threshold = threshold
        self.patience = patience
    }

    /// Feeds one observation. `speech` is the engine's voice-activity flag if it has one.
    mutating func feed(level: Float, speech: Bool? = nil, at t: TimeInterval) -> Decision {
        if startedAt == nil { startedAt = t }
        if speech ?? (level >= threshold) {
            heardSpeech = true
            lastSpeechAt = t
            return .keepListening
        }
        return decide(at: t)
    }

    /// Re-evaluates at time `t` without a new observation (a quiet tick).
    func decide(at t: TimeInterval) -> Decision {
        if heardSpeech, let last = lastSpeechAt {
            return t - last >= silence ? .endTake : .keepListening
        }
        if let start = startedAt, t - start >= patience { return .gaveUp }
        return .keepListening
    }

    /// 0 while speaking, rising to 1 as the silence cutoff nears — for a UI ring.
    func silenceProgress(at t: TimeInterval) -> Double {
        guard heardSpeech, let last = lastSpeechAt, silence > 0 else { return 0 }
        return min(max((t - last) / silence, 0), 1)
    }
}
