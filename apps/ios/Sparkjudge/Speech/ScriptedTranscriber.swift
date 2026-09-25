import Foundation

/// A transcriber that "hears" a fixed sentence with a synthetic voice level, so
/// the dictate screen can be demonstrated where there is no microphone (the
/// simulator, screenshots, UI tests). Enabled with the `-sjDemoDictation YES`
/// launch argument; never chosen otherwise.
actor ScriptedTranscriber: Transcriber {
    static let defaultScript = "An app that turns voice memos from a walk into a weekly letter to yourself, with the best ideas pulled out and sorted by what you could start on this weekend"

    let script: String
    let wordInterval: Duration
    private var task: Task<Void, Never>?
    private var continuation: AsyncThrowingStream<TranscriberEvent, any Error>.Continuation?

    init(script: String = ScriptedTranscriber.defaultScript, wordInterval: Duration = .milliseconds(260)) {
        self.script = script
        self.wordInterval = wordInterval
    }

    func start(locale: Locale) async -> AsyncThrowingStream<TranscriberEvent, any Error> {
        let (stream, continuation) = AsyncThrowingStream.makeStream(of: TranscriberEvent.self, throwing: (any Error).self)
        self.continuation = continuation
        let words = script.split(separator: " ").map(String.init)
        let interval = wordInterval
        task = Task {
            continuation.yield(.listening)
            var said: [String] = []
            var phase = 0.0
            for word in words {
                guard !Task.isCancelled else { break }
                said.append(word)
                // A few level samples per word: loud on the vowel, softer between.
                for i in 0..<4 {
                    phase += 0.9
                    let level = Float(0.45 + 0.35 * abs(sin(phase)) + (i == 1 ? 0.15 : 0))
                    continuation.yield(.level(min(level, 1)))
                    try? await Task.sleep(for: interval / 4)
                }
                let settled = said.count > 4 ? said.dropLast(3).joined(separator: " ") : ""
                let guess = said.suffix(min(3, said.count)).joined(separator: " ")
                continuation.yield(.transcript(finalized: settled, volatile: settled.isEmpty ? guess : " " + guess))
            }
            continuation.yield(.transcript(finalized: said.joined(separator: " ") + ".", volatile: ""))
            // Then quiet, so the silence gate ends the take.
            while !Task.isCancelled {
                continuation.yield(.level(0.02))
                continuation.yield(.speech(false))
                try? await Task.sleep(for: .milliseconds(50))
            }
        }
        return stream
    }

    func stop() async {
        task?.cancel()
        continuation?.finish()
        continuation = nil
    }

    func cancel() async { await stop() }
}
