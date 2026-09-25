import AVFAudio
import Foundation

/// Loudness from raw samples. Pure functions, so they can be tested without a microphone.
enum LevelMeter {
    /// Root-mean-square of the samples.
    static func rms(_ samples: UnsafeBufferPointer<Float>) -> Float {
        guard !samples.isEmpty else { return 0 }
        var sum: Float = 0
        for s in samples { sum += s * s }
        return (sum / Float(samples.count)).squareRoot()
    }

    /// Maps an RMS value onto [0, 1] through decibels: `floor` dBFS and below is
    /// 0, 0 dBFS is 1. Speech usually sits around -30…-10 dBFS.
    static func normalized(rms: Float, floor: Float = -55) -> Float {
        guard rms > 0 else { return 0 }
        let db = 20 * log10(rms)
        return min(max((db - floor) / -floor, 0), 1)
    }

    /// The normalized level of a PCM buffer's first channel.
    static func level(of buffer: AVAudioPCMBuffer) -> Float {
        guard let data = buffer.floatChannelData, buffer.frameLength > 0 else { return 0 }
        return normalized(rms: rms(UnsafeBufferPointer(start: data[0], count: Int(buffer.frameLength))))
    }
}

/// One-pole smoothing with separate attack and release times: the orb jumps
/// up when you speak and settles slowly when you stop.
struct LevelSmoother: Sendable {
    var attack: Double = 0.05
    var release: Double = 0.35
    private(set) var value: Float = 0

    init(attack: Double = 0.05, release: Double = 0.35) {
        self.attack = attack
        self.release = release
    }

    mutating func next(_ target: Float, dt: Double) -> Float {
        let tau = target > value ? attack : release
        let k = Float(1 - exp(-max(dt, 0) / max(tau, 0.0001)))
        value += (target - value) * k
        return value
    }
}
