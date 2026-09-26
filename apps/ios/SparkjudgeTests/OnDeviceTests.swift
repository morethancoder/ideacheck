import Foundation
import LayaKit
import Sparkcore
import Testing
@testable import Sparkjudge

/// A deterministic Swift judge: the first option, the top level, "probably
/// true" — what the core sees from FoundationJudge or LayaDeviceJudge.
final class StubJudge: NSObject, SparkcoreJudgeProtocol, @unchecked Sendable {
    let delay: TimeInterval
    private let lock = NSLock()
    private(set) var asked = 0

    init(delay: TimeInterval = 0) {
        self.delay = delay
    }

    func name() -> String { "stub" }

    func evaluate(_ requestJSON: String?, error: NSErrorPointer) -> String {
        lock.withLock { asked += 1 }
        if delay > 0 { Thread.sleep(forTimeInterval: delay) }
        guard let data = requestJSON?.data(using: .utf8),
            let request = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
            let q = request["question"] as? [String: Any]
        else { return "{}" }
        switch q["kind"] as? String {
        case "choice":
            let first = ((q["options"] as? [[String: Any]])?.first?["key"] as? String) ?? "other"
            return #"{"choice": "\#(first)"}"#
        case "score":
            return #"{"level": \#(((q["levels"] as? [Any])?.count ?? 1) - 1)}"#
        default:
            return #"{"probability": 0.8}"#
        }
    }
}

let dentalIntake = Intake(idea: "A scheduling app for dental clinics that fills cancelled slots from a waitlist by text message.",
                          fields: ["audience": "independent dental clinics"])

@Suite struct OnDeviceCheckTests {
    @Test func theCoreRunsAndItsResultDecodesIntoCheckResult() async throws {
        let checker = OnDeviceChecker(judge: .apple, writer: false, swiftJudge: StubJudge())
        var progress: [CheckProgress] = []
        var result: CheckResult?
        for try await event in checker.check(dentalIntake, rubric: "business") {
            switch event {
            case .progress(let p): progress.append(p)
            case .result(let r, let raw):
                result = r
                #expect(try CheckResult.decode(raw) == r)
            case .accepted: break
            }
        }
        let r = try #require(result)
        #expect(r.status == "ok")
        #expect(r.backend == "stub")
        #expect(r.rubric?.name == "business")
        #expect(r.verdictValue != nil)
        // Every score event carries the stage's size, and it is the number
        // of score questions that finished.
        let scored = progress.filter { $0.stage == "score" && $0.isFinished }
        #expect(!scored.isEmpty)
        #expect(scored.allSatisfy { $0.expected == scored.count })
    }

    @Test func noJudgeExplainsHowToGetOne() async {
        let checker = OnDeviceChecker(judge: nil, writer: true)
        await #expect(throws: OnDeviceError.noJudge) {
            for try await _ in checker.check(dentalIntake, rubric: nil) {}
        }
    }

    @Test func cancellingStopsTheCheckAtOnce() async throws {
        let checker = OnDeviceChecker(judge: .apple, writer: false, swiftJudge: StubJudge(delay: 2))
        let start = Date()
        let task = Task {
            for try await _ in checker.check(dentalIntake, rubric: nil) {}
        }
        try await Task.sleep(for: .milliseconds(200))
        task.cancel()
        _ = await task.result
        #expect(Date().timeIntervalSince(start) < 3, "the stream ended when the task was cancelled, not after the questions")
    }

    @Test func settingsAreOnesTheCoreAccepts() throws {
        let laya = EngineSettings.make(judge: .laya(id: "aac6fef/laya-typed-decisions-coreml", directory: URL(fileURLWithPath: "/m")), writer: true)
        #expect(laya.json == #"{"explain":true,"extract":true,"max_concurrent":1,"model":"aac6fef\/laya-typed-decisions-coreml","question_seconds":60,"writer_model":"apple-foundation-model","writer_seconds":180}"#)
        let apple = EngineSettings.make(judge: .apple, writer: false)
        #expect(apple.json == #"{"explain":false,"extract":false,"max_concurrent":1,"model":"apple-foundation-model","question_seconds":60,"writer_seconds":600}"#)
        // The core reads settings strictly: an unknown key is an error.
        for s in [laya, apple] {
            _ = try SparkcoreEngine.make(settingsJSON: s.json, judge: StubJudge())
        }
        #expect(OnDeviceRun.options(rubric: nil) == "")
        #expect(OnDeviceRun.options(rubric: "creative") == #"{"rubric":"creative"}"#)
    }
}

@Suite struct ProgressTests {
    func event(_ type: String, _ stage: String, _ id: String, answer: String = "") -> String {
        #"{"type":"\#(type)","stage":"\#(stage)","question":{"id":"\#(id)","kind":"noul","instructions":"…","weight":1,"polarity":1,"uses":["idea"]}\#(answer)}"#
    }

    @Test func coreEventsBecomeProgress() throws {
        let p = try #require(SparkcoreEvent.progress(event("answered", "score", "sisp",
            answer: #","answer":{"id":"sisp","kind":"noul","noul":0.8,"probabilities":{"yes":0.8,"no":0.2},"confidence":0.6,"method":"stub","model":"stub","latency_ms":12},"value":0.8"#)))
        #expect(p.stage == "score" && p.isFinished && p.question.id == "sisp")
        #expect(p.answer?.noul == 0.8 && p.value == 0.8)
        // The writer's steps carry answers without probabilities: the step
        // still counts, the answer is dropped.
        let extract = try #require(SparkcoreEvent.progress(#"{"type":"answered","stage":"extract","question":{"id":"extract","kind":"","instructions":"Read"},"answer":{"id":"extract","kind":"","probabilities":null,"confidence":0,"method":"","model":"m","latency_ms":900}}"#))
        #expect(extract.stage == "extract" && extract.answer == nil)
        #expect(SparkcoreEvent.progress("not json") == nil)
    }

    @Test func aRunNamesItsStage() throws {
        var run = CheckRun()
        let steps: [(String, String, String, Int?)] = [
            ("started", "load", "judge", nil), ("answered", "load", "judge", nil),
            ("started", "extract", "extract", nil), ("answered", "extract", "extract", nil),
            ("started", "preflight", "has_problem", nil), ("answered", "preflight", "has_problem", nil),
            ("started", "score", "sisp", 3), ("answered", "score", "sisp", 3),
            ("started", "score", "tarpit", 3),
        ]
        var labels: [String] = []
        var fractions: [Double] = []
        for (type, stage, id, expected) in steps {
            var p = try #require(SparkcoreEvent.progress(event(type, stage, id)))
            p.expected = expected
            run.apply(p)
            labels.append(run.label)
            fractions.append(run.fraction)
        }
        #expect(labels.first == "Loading the judge")
        #expect(labels.contains("Reading your idea"))
        #expect(labels.last == "Scoring 2 of 3")
        #expect(run.question == "tarpit")
        #expect(fractions == fractions.sorted(), "the bar never goes back")
        run.apply(try #require(SparkcoreEvent.progress(event("started", "explain", "summary"))))
        #expect(run.label == "Summarising")
        #expect(run.question == nil)
    }

    @Test func theScoringStageKnowsItsSize() throws {
        let rubrics = [
            "business": [ScorePlan.Question(id: "a", requires: []), .init(id: "b", requires: ["evidence"]),
                         .init(id: "c", requires: ["profile"]), .init(id: "clarity", requires: [])],
            "creative": [ScorePlan.Question(id: "x", requires: []), .init(id: "clarity", requires: [])],
        ]
        // Chosen in Review: known before anything is asked. Evidence is never
        // there on the phone; the profile question counts only with a profile.
        #expect(ScorePlan(rubrics: rubrics, rubric: "business", hasProfile: false).expected == 2)
        #expect(ScorePlan(rubrics: rubrics, rubric: "business", hasProfile: true).expected == 3)

        // Routed: the router's answer names the rubric.
        var routed = ScorePlan(rubrics: rubrics, rubric: nil, hasProfile: false)
        let route = try #require(SparkcoreEvent.progress(#"{"type":"answered","stage":"preflight","question":{"id":"idea_type"},"answer":{"id":"idea_type","kind":"choice","choice":"creative","probabilities":{"creative":1},"confidence":1,"method":"stub","model":"stub","latency_ms":1}}"#))
        #expect(routed.see(route) == 2)

        // "other" names no rubric: the questions scored tell it apart, once
        // only one rubric holds them all.
        var fallback = ScorePlan(rubrics: rubrics, rubric: nil, hasProfile: false)
        #expect(fallback.see(try #require(SparkcoreEvent.progress(event("started", "score", "clarity")))) == nil)
        #expect(fallback.see(try #require(SparkcoreEvent.progress(event("started", "score", "a")))) == 2)
    }

    @Test func theCoresOwnRubricsParse() throws {
        let rubrics = ScorePlan.parse(try sparkcoreRubrics())
        #expect(rubrics["business"]?.isEmpty == false)
        #expect(rubrics["business"]?.contains { $0.requires.contains("evidence") } == true)
    }
}

@Suite struct JudgeChoiceTests {
    let dir = URL(fileURLWithPath: "/models")
    var typed: LocalCheckpoint { LocalCheckpoint(id: "aac6fef/laya-typed-decisions-coreml", directory: dir.appending(path: "t")) }
    var multilingual: LocalCheckpoint { LocalCheckpoint(id: "aac6fef/laya-multilingual-coreml", directory: dir.appending(path: "m")) }
    var ane: LocalCheckpoint {
        LocalCheckpoint(id: "aac6fef/laya-multilingual-coreml-ane", directory: dir.appending(path: "a"),
                        problem: LayaFit.problem(maxLength: 96, maxOptions: 32))
    }

    @Test func automaticPrefersTypedDecisionsThenAppleThenAnyLaya() {
        #expect(JudgeResolver.resolve(.automatic, appleAvailable: true, downloaded: [multilingual, typed]) == .laya(id: typed.id, directory: typed.directory))
        #expect(JudgeResolver.resolve(.automatic, appleAvailable: true, downloaded: [multilingual]) == .apple)
        #expect(JudgeResolver.resolve(.automatic, appleAvailable: false, downloaded: [multilingual]) == .laya(id: multilingual.id, directory: multilingual.directory))
        #expect(JudgeResolver.resolve(.automatic, appleAvailable: false, downloaded: []) == nil)
    }

    @Test func aChoiceStandsWhileItIsUsable() {
        #expect(JudgeResolver.resolve(.laya(multilingual.id), appleAvailable: true, downloaded: [typed, multilingual]) == .laya(id: multilingual.id, directory: multilingual.directory))
        #expect(JudgeResolver.resolve(.apple, appleAvailable: true, downloaded: [typed]) == .apple)
        // Removed, or Apple Intelligence turned off: the next best.
        #expect(JudgeResolver.resolve(.laya(multilingual.id), appleAvailable: true, downloaded: [typed]) == .laya(id: typed.id, directory: typed.directory))
        #expect(JudgeResolver.resolve(.apple, appleAvailable: false, downloaded: [typed]) == .laya(id: typed.id, directory: typed.directory))
    }

    @Test func aCheckpointThatCannotHoldAnIdeaIsNeverTheJudge() {
        #expect(JudgeResolver.resolve(.laya(ane.id), appleAvailable: false, downloaded: [ane]) == nil)
        #expect(LayaFit.problem(maxLength: 64, maxOptions: 4) != nil)
        #expect(LayaFit.problem(maxLength: 1024, maxOptions: 32) == nil)
        #expect(LayaFit.problem(maxLength: 1024, maxOptions: 4, neededOptions: 6) != nil)
        // The core's rubrics ask up to a score's levels and the router's six kinds.
        #expect(LayaFit.neededOptions >= 6)
    }

    @Test func preferencesRoundTripThroughTheirStoredString() {
        for p in [JudgePreference.automatic, .apple, .laya("aac6fef/laya-coreml")] {
            #expect(JudgePreference(rawValue: p.rawValue) == p)
        }
        #expect(JudgePreference(rawValue: nil) == .automatic)
        #expect(JudgePreference(rawValue: "laya:") == .automatic)
    }

    @Test func freeUsersCheckOnThePhoneAndProInTheCloudUnlessTheyChose() {
        #expect(CheckRoute.resolve(stored: nil, isPro: false) == .onDevice)
        #expect(CheckRoute.resolve(stored: nil, isPro: true) == .remote)
        // A free user who picked Cloud spends the monthly free checks.
        #expect(CheckRoute.resolve(stored: .remote, isPro: false) == .remote)
        #expect(CheckRoute.resolve(stored: .onDevice, isPro: true) == .onDevice)
    }

    @Test func reviewOffersAJudgeOnlyWhenTheCheckWouldRunHere() {
        #expect(ReviewView.offer(route: .remote, judge: nil, hasLaya: false, offered: false) == nil)
        #expect(ReviewView.offer(route: .onDevice, judge: nil, hasLaya: false, offered: true) == .noJudge)
        #expect(ReviewView.offer(route: .onDevice, judge: .apple, hasLaya: false, offered: false) == .faster)
        #expect(ReviewView.offer(route: .onDevice, judge: .apple, hasLaya: false, offered: true) == nil)
        #expect(ReviewView.offer(route: .onDevice, judge: .laya(id: "x", directory: dir), hasLaya: true, offered: false) == nil)
    }
}

@Suite struct OnDeviceErrorTests {
    @Test func cancellingIsNotAFailure() {
        let core = NSError(domain: "go", code: 1, userInfo: [NSLocalizedDescriptionKey: "sparkcore: check cancelled: context canceled"])
        #expect(OnDeviceError.friendly(core, judge: .apple) is CancellationError)
        #expect(OnDeviceError.friendly(CancellationError(), judge: .apple) is CancellationError)
    }

    @Test func errorsReadAsWhatToDo() {
        let laya = OnDeviceError.friendly(LayaError.model("the model returned no logits"), judge: .laya(id: "x", directory: URL(fileURLWithPath: "/")))
        #expect(laya as? OnDeviceError == .layaFailed("laya: the model returned no logits"))
        let core = NSError(domain: "go", code: 1, userInfo: [NSLocalizedDescriptionKey: "sparkcore: rubric business: bad gate\nstack…"])
        let e = OnDeviceError.friendly(core, judge: .apple)
        #expect(e.localizedDescription == "The check stopped on this iPhone: rubric business: bad gate")
        #expect(OnDeviceError.noJudge.localizedDescription.contains("Download Laya"))
    }

    @Test func aResultWithoutAScoreSaysWhyFromItsAnswers() throws {
        var result = try CheckResult.decode(try fixture("check_result_mock"))
        result.status = "error"
        result.error = "no weighted question was answered; see answers[].error"
        for i in result.answers.indices { result.answers[i].error = "the on-device model is unavailable: Apple Intelligence is turned off in Settings" }
        result.answers[0].error = "no profile given"
        #expect(OnDeviceError.failed(result) == .appleUnavailable("Apple Intelligence is turned off in Settings"))
        for i in result.answers.indices { result.answers[i].error = nil }
        #expect(OnDeviceError.failed(result) == .noScore("no weighted question was answered; see answers[].error"))
    }

    func fixture(_ name: String) throws -> Data {
        let url = try #require(Bundle(for: StubJudge.self).url(forResource: name, withExtension: "json"))
        return try Data(contentsOf: url)
    }
}
