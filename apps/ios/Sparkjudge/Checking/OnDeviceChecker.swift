import Foundation

/// The seam for checking without a server: the Go engine built with gomobile,
/// with Apple's on-device model (Foundation Models) answering the typed
/// questions as the judge. Not built yet — it reports itself unavailable, and
/// Settings shows it as coming later.
///
/// When it lands, it implements the same stream as `RemoteChecker`: the engine's
/// `pipeline.Event`s map onto `CheckEvent.progress`, its result JSON onto
/// `CheckEvent.result`, so nothing above this file changes.
struct OnDeviceChecker: IdeaChecker {
    var kind: CheckerKind { .onDevice }

    static let isAvailable = false

    func check(_ intake: Intake, rubric: String?) -> AsyncThrowingStream<CheckEvent, any Error> {
        AsyncThrowingStream { $0.finish(throwing: CheckError.unavailable("Checking on this iPhone is coming in a later version. Pick the ideacheck server or the offline preview in Settings.")) }
    }
}
