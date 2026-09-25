// gomobile declares a Go `(string, error)` as an Objective-C method returning a
// non-null NSString plus an NSError pointer, which Swift does not import as
// `throws`. These overloads give the app the Swift shape.

import Foundation
import Sparkcore

// An Engine and a Check may be used from any thread (mobile/sparkcore.go):
// Cancel is meant to come from another thread than Run.
extension SparkcoreEngine: @retroactive @unchecked Sendable {}
extension SparkcoreCheck: @retroactive @unchecked Sendable {}

func rethrowing(_ body: (NSErrorPointer) -> String) throws -> String {
    var error: NSError?
    let out = body(&error)
    if let error { throw error }
    return out
}

extension SparkcoreEngine {
    /// Runs one check start to finish and returns the result JSON.
    public func check(_ intakeJSON: String, listener: (any SparkcoreListenerProtocol)?) throws -> String {
        try rethrowing { check(intakeJSON, listener: listener, error: $0) }
    }

    /// Builds an engine around a judge and an optional writer.
    public static func make(settingsJSON: String = "", judge: any SparkcoreJudgeProtocol,
                            writer: (any SparkcoreWriterProtocol)? = nil) throws -> SparkcoreEngine {
        var error: NSError?
        let engine = SparkcoreNewEngine(settingsJSON, judge, writer, &error)
        if let error { throw error }
        guard let engine else { throw NSError(domain: "sparkcore", code: 2) }
        return engine
    }

    /// Builds an engine around a judge that also answers a group of questions
    /// in one call (settings {"batch": false} asks one at a time anyway).
    public static func make(settingsJSON: String = "", batchJudge: any SparkcoreBatchJudgeProtocol,
                            writer: (any SparkcoreWriterProtocol)? = nil) throws -> SparkcoreEngine {
        var error: NSError?
        let engine = SparkcoreNewBatchEngine(settingsJSON, batchJudge, writer, &error)
        if let error { throw error }
        guard let engine else { throw NSError(domain: "sparkcore", code: 2) }
        return engine
    }
}

extension SparkcoreCheck {
    /// Runs the check (off the main thread) and returns the result JSON.
    public func run(_ listener: (any SparkcoreListenerProtocol)?) throws -> String {
        try rethrowing { run(listener, error: $0) }
    }
}

/// The field catalogue and the scoring rubrics, as JSON, for the app's forms.
public func sparkcoreFields() throws -> String { try rethrowing { SparkcoreFields($0) } }
public func sparkcoreRubrics() throws -> String { try rethrowing { SparkcoreRubrics($0) } }
