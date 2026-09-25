import Foundation
import SwiftData
import Observation

/// Runs checks and writes their results onto ideas. Main-actor, like SwiftData's
/// main context it writes to.
@MainActor
@Observable
final class CheckCoordinator {
    private(set) var runs: [UUID: CheckRun] = [:]
    private var tasks: [UUID: Task<Void, Never>] = [:]
    /// Makes the checker for one check. nil = the one Settings and the plan
    /// choose (CheckRoute); the app wires in the plan and this iPhone's judge.
    var makeChecker: @MainActor (CheckerKind?) -> any IdeaChecker = { kind in
        AppSettings.makeChecker(kind ?? AppSettings.checker, onDevice: OnDeviceChecker(judge: nil, writer: false))
    }

    func isChecking(_ idea: Idea) -> Bool { runs[idea.id] != nil }

    /// - Parameter kind: check with this checker once, whatever Settings says
    ///   (Review's "Check with Sparkjudge Cloud"); nil = Settings' choice.
    func check(_ idea: Idea, in context: ModelContext, with kind: CheckerKind? = nil) {
        tasks[idea.id]?.cancel()
        let id = idea.id
        let intake = idea.intake
        let rubric = idea.categoryChosen ? idea.category.rubric : nil
        let checker = makeChecker(kind)
        idea.status = .checking
        idea.lastError = nil
        try? context.save()
        runs[id] = CheckRun()

        tasks[id] = Task { [weak self] in
            do {
                for try await event in checker.check(intake, rubric: rubric) {
                    guard let self else { return }
                    switch event {
                    case .accepted:
                        break
                    case .progress(let p):
                        var run = self.runs[id] ?? CheckRun()
                        run.apply(p)
                        self.runs[id] = run
                    case .result(let result, let raw):
                        idea.apply(result, raw: raw)
                        try? context.save()
                    }
                }
                if idea.status == .checking { throw CheckError.streamEnded }
            } catch is CancellationError {
                idea.status = idea.resultData == nil ? .reviewed : .checked
            } catch {
                idea.status = idea.resultData == nil ? .failed : .checked
                idea.lastError = error.localizedDescription
            }
            try? context.save()
            self?.runs[id] = nil
            self?.tasks[id] = nil
        }
    }

    func cancel(_ idea: Idea) {
        tasks[idea.id]?.cancel()
    }
}
