import Foundation
import Testing
@testable import Sparkjudge

struct SilenceGateTests {
    /// Feeds (time, level) pairs and returns the first time the gate did not say keep listening.
    private func run(_ gate: inout SilenceGate, _ samples: [(TimeInterval, Float)]) -> (TimeInterval, SilenceGate.Decision)? {
        for (t, level) in samples {
            let d = gate.feed(level: level, at: t)
            if d != .keepListening { return (t, d) }
        }
        return nil
    }

    private func samples(from start: TimeInterval, to end: TimeInterval, level: Float, step: TimeInterval = 0.05) -> [(TimeInterval, Float)] {
        stride(from: start, to: end, by: step).map { ($0, level) }
    }

    @Test func endsAfterSilenceFollowingSpeech() throws {
        var gate = SilenceGate(silence: 2)
        let stream = samples(from: 0, to: 3, level: 0.6) + samples(from: 3, to: 8, level: 0.05)
        let (t, decision) = try #require(run(&gate, stream))
        #expect(decision == .endTake)
        #expect(abs(t - 4.95) < 0.11) // last speech at 2.95 + 2 s
    }

    @Test func aPauseShorterThanTheCutoffDoesNotEnd() {
        var gate = SilenceGate(silence: 2)
        let stream = samples(from: 0, to: 1, level: 0.6) + samples(from: 1, to: 2.5, level: 0.05)
            + samples(from: 2.5, to: 3.5, level: 0.7) + samples(from: 3.5, to: 5.4, level: 0.05)
        #expect(run(&gate, stream) == nil)
        #expect(gate.decide(at: 5.6) == .endTake)
    }

    @Test func silenceBeforeAnySpeechWaitsThenGivesUp() throws {
        var gate = SilenceGate(silence: 2, patience: 10)
        #expect(run(&gate, samples(from: 0, to: 9.9, level: 0.05)) == nil)
        let (_, decision) = try #require(run(&gate, samples(from: 9.9, to: 11, level: 0.05)))
        #expect(decision == .gaveUp)
    }

    @Test func voiceActivityOverridesLevel() {
        var gate = SilenceGate(silence: 1)
        // Loud but not speech (a fan): never counts as speech.
        for t in stride(from: 0.0, to: 3, by: 0.1) { _ = gate.feed(level: 0.9, speech: false, at: t) }
        #expect(!gate.heardSpeech)
        // Quiet but speech (a whisper): counts.
        _ = gate.feed(level: 0.05, speech: true, at: 3)
        #expect(gate.heardSpeech)
        #expect(gate.feed(level: 0.05, speech: false, at: 3.5) == .keepListening)
        #expect(gate.feed(level: 0.05, speech: false, at: 4.0) == .endTake)
    }

    @Test func silenceProgressRisesToOne() {
        var gate = SilenceGate(silence: 2)
        _ = gate.feed(level: 0.8, at: 1)
        #expect(gate.silenceProgress(at: 1) == 0)
        #expect(abs(gate.silenceProgress(at: 2) - 0.5) < 1e-9)
        #expect(gate.silenceProgress(at: 9) == 1)
    }

    @Test func thresholdIsInclusive() {
        var gate = SilenceGate(silence: 1, threshold: 0.3)
        _ = gate.feed(level: 0.3, at: 0)
        #expect(gate.heardSpeech)
    }
}

struct LevelTests {
    @Test func smootherAttacksFastAndReleasesSlowly() {
        var s = LevelSmoother(attack: 0.05, release: 0.5)
        let up = s.next(1, dt: 0.05)            // one attack time constant
        #expect(abs(up - Float(1 - exp(-1.0))) < 0.001)
        var r = LevelSmoother(attack: 0.05, release: 0.5)
        for _ in 0..<40 { _ = r.next(1, dt: 0.05) }
        let down = r.next(0, dt: 0.05)          // a tenth of the release time
        #expect(down > 0.85)
    }

    @Test func rmsAndDecibelNormalization() {
        let samples: [Float] = [0.5, -0.5, 0.5, -0.5]
        let rms = samples.withUnsafeBufferPointer { LevelMeter.rms($0) }
        #expect(abs(rms - 0.5) < 1e-6)
        #expect(LevelMeter.normalized(rms: 1) == 1)
        #expect(LevelMeter.normalized(rms: 0) == 0)
        #expect(LevelMeter.normalized(rms: 0.0001) == 0) // -80 dBFS is below the floor
        let speech = LevelMeter.normalized(rms: pow(10, -20.0 / 20)) // -20 dBFS
        #expect(abs(speech - Float(35.0 / 55.0)) < 0.001)
    }
}
