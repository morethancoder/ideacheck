import Foundation
import Testing
@testable import Sparkjudge

private final class BundleToken {}

enum Fixture {
    /// `go run ./cmd/ideacheck "a to-do app for dentists" -b mock --agent`, saved verbatim.
    static func mockResult() throws -> Data {
        let url = try #require(Bundle(for: BundleToken.self).url(forResource: "check_result_mock", withExtension: "json"))
        return try Data(contentsOf: url)
    }
}

struct CheckResultTests {
    @Test func decodesARealEngineResult() throws {
        let r = try CheckResult.decode(Fixture.mockResult())
        #expect(r.status == "ok")
        #expect(r.id == "chk_01M3CX7MXGFB6QVCFTHY7MDTQH")
        #expect(r.backend == "mock")
        #expect(r.ideaType == .init(choice: "side_project", confidence: 0.8))
        #expect(r.verdict == "park")
        #expect(r.verdictValue == .park)
        #expect(r.rubric?.name == "side_project")
        #expect(r.dimensions.count == 8)
        #expect(r.answers.contains { $0.id == "idea_type" && $0.choice == "side_project" })
        #expect(r.topStrengths.first?.id == "learning_value")
        #expect(r.timing.totalMs == 24811)
        #expect(r.cost.basis == "reported")
        #expect(r.missing.isEmpty)
        #expect(r.summary?.isEmpty == false)
    }

    @Test func nullDimensionValueIsNotScored() throws {
        let r = try CheckResult.decode(Fixture.mockResult())
        let skipped = try #require(r.dimensions.first { $0.id == "scope_fits_time" })
        #expect(skipped.value == nil)
        #expect(skipped.goodness == nil)
        #expect(skipped.error == "no profile given")
    }

    @Test func polarityTurnsValuesIntoGoodNews() throws {
        let r = try CheckResult.decode(Fixture.mockResult())
        let bad = try #require(r.dimensions.first { $0.id == "existing_tool_suffices" })
        #expect(bad.polarity == -1)
        #expect(abs((bad.goodness ?? 0) - (1 - 0.84588)) < 0.0001)
    }

    @Test func ratingIsCompositeTimesTenOneDecimal() throws {
        let r = try CheckResult.decode(Fixture.mockResult())
        #expect(r.rating == 4.4) // composite 0.44488…
        #expect(CheckResult.rating(0.745) == 7.5)
        #expect(CheckResult.rating(1.2) == 10)
        #expect(CheckResult.rating(-1) == 0)
    }

    @Test func roundTripKeepsSchemaFieldNames() throws {
        let r = try CheckResult.decode(Fixture.mockResult())
        let json = try #require(try JSONSerialization.jsonObject(with: r.encoded()) as? [String: Any])
        for key in ["composite_confidence", "verdict_reason", "top_strengths", "top_risks", "cost_estimate_usd", "config_hash", "created_at", "idea_type"] {
            #expect(json[key] != nil, "missing \(key)")
        }
        let dims = try #require(json["dimensions"] as? [[String: Any]])
        // A dimension that was not scored still carries "value": null, as the schema requires.
        #expect(dims.contains { $0["id"] as? String == "scope_fits_time" && $0["value"] is NSNull })
        #expect(try CheckResult.decode(r.encoded()) == r)
    }

    @Test func applyingAResultUpdatesTheIdea() throws {
        let raw = try Fixture.mockResult()
        let idea = Idea(transcript: "a to-do app for dentists")
        idea.apply(try CheckResult.decode(raw), raw: raw)
        #expect(idea.status == .checked)
        #expect(idea.verdict == .park)
        #expect(idea.category == .sideProject) // the router's choice, since none was chosen
        #expect(idea.result?.id == "chk_01M3CX7MXGFB6QVCFTHY7MDTQH")

        let chosen = Idea(transcript: "x", category: .creative)
        chosen.categoryChosen = true
        chosen.apply(try CheckResult.decode(raw), raw: raw)
        #expect(chosen.category == .creative)
    }

    @Test func verdictThresholds() {
        #expect(Verdict.from(composite: 0.7) == .build)
        #expect(Verdict.from(composite: 0.69) == .explore)
        #expect(Verdict.from(composite: 0.5) == .explore)
        #expect(Verdict.from(composite: 0.3) == .park)
        #expect(Verdict.from(composite: 0.29) == .kill)
    }
}

struct PreviewCheckerTests {
    @Test func sameIdeaSameResult() throws {
        let template = try CheckResult.decode(Fixture.mockResult())
        let intake = Intake(idea: "a to-do app for dentists", fields: ["title": "Chairside"])
        let a = try PreviewChecker.result(for: intake, rubric: nil, template: template)
        let b = try PreviewChecker.result(for: intake, rubric: nil, template: template)
        #expect(a == b)
        let other = try PreviewChecker.result(for: Intake(idea: "a podcast about bridges"), rubric: nil, template: template)
        #expect(other.id != a.id)
    }

    @Test func honoursTheRubricAndItsOwnVerdict() throws {
        let template = try CheckResult.decode(Fixture.mockResult())
        for c in IdeaCategory.allCases where c != .other {
            let r = try PreviewChecker.result(for: Intake(idea: "idea \(c)"), rubric: c.rawValue, template: template)
            #expect(r.ideaType?.choice == c.rawValue)
            #expect(r.verdictValue == Verdict.from(composite: r.composite))
            #expect((0...1).contains(r.composite))
            #expect(!r.dimensions.isEmpty)
        }
    }

    @Test func streamEndsWithOneResult() async throws {
        var results = 0
        var progress = 0
        for try await e in PreviewChecker(step: .zero).check(Intake(idea: "plant pager"), rubric: "side_project") {
            switch e {
            case .result: results += 1
            case .progress: progress += 1
            case .accepted: #expect(results == 0)
            }
        }
        #expect(results == 1)
        #expect(progress > 0)
    }
}
