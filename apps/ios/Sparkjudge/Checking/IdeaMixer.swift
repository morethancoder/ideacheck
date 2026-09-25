import Foundation
import FoundationModels

/// Writes one new idea out of two or three. Free text with no option list, so
/// it is the writer's job: Apple's on-device model on the phone, the hosted
/// API's writer for Pro, or a stitched preview for demos.
protocol IdeaMixer: Sendable {
    /// Shown under the Mix button: who writes.
    var label: String { get }
    var availability: WriterAvailability { get }
    func mix(_ ideas: [MixInput]) async throws -> MixedIdea
}

/// What the on-device model fills in. Field names follow `configs/fields.yaml`.
@Generable(description: "A new idea that combines the ideas given")
struct MixNotes {
    @Guide(description: "A short, memorable name for the new idea: two to five words, no quotes, no trailing period")
    var title: String

    @Guide(description: "The specific problem the new idea solves and for whom, in one sentence")
    var problem: String

    @Guide(description: "The first users of the new idea, narrowly, in one sentence")
    var audience: String

    @Guide(description: "What would actually be built or made, in one sentence")
    var solution: String
}

/// The writer on the phone. Its instructions and prompt are bundled files
/// (`mixer_instructions.md`, `mixer_prompt.md`), as the engine's prompts are
/// config files, so wording changes never touch Swift.
struct OnDeviceMixer: IdeaMixer {
    var label: String { "Written on this iPhone by Apple Intelligence" }

    var availability: WriterAvailability { OnDeviceWriter().availability }

    func mix(_ ideas: [MixInput]) async throws -> MixedIdea {
        let session = LanguageModelSession(instructions: Self.instructions())
        let response = try await session.respond(to: Self.prompt(ideas), generating: MixNotes.self,
                                                 options: GenerationOptions(temperature: 0.7))
        let n = response.content
        return MixedIdea(title: n.title, problem: n.problem, audience: n.audience, solution: n.solution)
    }

    static func instructions(bundle: Bundle = .main) -> String {
        let base = resource("mixer_instructions", bundle: bundle) ?? ""
        let fields = FieldCatalog.shared.idea
            .filter { ["problem", "audience", "solution"].contains($0.name) }
            .map { "- \($0.name): \($0.description)" }.joined(separator: "\n")
        return base + "\n\nWhat each field means:\n" + fields
    }

    /// `mixer_prompt.md` with `{{ideas}}` replaced by each idea in turn.
    static func prompt(_ ideas: [MixInput], bundle: Bundle = .main) -> String {
        let template = resource("mixer_prompt", bundle: bundle) ?? "{{ideas}}"
        let blocks = ideas.enumerated().map { i, idea in
            var lines = ["IDEA \(i + 1): \(idea.title)", idea.idea]
            lines += idea.fields.sorted { $0.key < $1.key }.map { "\($0.key): \($0.value)" }
            return lines.filter { !$0.isEmpty }.joined(separator: "\n")
        }
        return template.replacingOccurrences(of: "{{ideas}}", with: blocks.joined(separator: "\n\n"))
    }

    private static func resource(_ name: String, bundle: Bundle) -> String? {
        bundle.url(forResource: name, withExtension: "md").flatMap { try? String(contentsOf: $0, encoding: .utf8) }
    }
}

/// The hosted API's writer (`POST /v1/mix`), for Pro.
struct RemoteMixer: IdeaMixer {
    var client: LabClient
    var label: String { "Written by Sparkjudge's writer (Pro)" }
    var availability: WriterAvailability { .available }

    func mix(_ ideas: [MixInput]) async throws -> MixedIdea {
        try await client.mix(ideas).mixed
    }
}

/// A deterministic stand-in for demos and screenshots (`-checker preview`):
/// it stitches the ideas' own words together, and says so.
struct PreviewMixer: IdeaMixer {
    var label: String { "Preview: stitched from your own words, no model" }
    var availability: WriterAvailability { .available }

    func mix(_ ideas: [MixInput]) async throws -> MixedIdea {
        func first(_ key: String) -> String? { ideas.lazy.compactMap { $0.fields[key] }.first { !$0.isEmpty } }
        let names = ideas.map { $0.title.split(separator: " ").prefix(2).joined(separator: " ") }
        return MixedIdea(
            title: names.joined(separator: " × "),
            problem: first("problem") ?? "",
            audience: first("audience") ?? "",
            solution: ideas.map(\.idea).filter { !$0.isEmpty }.map { $0.hasSuffix(".") ? String($0.dropLast()) : $0 }.joined(separator: ", meeting ")
        )
    }
}

enum MixerChoice {
    /// On the phone when Apple Intelligence can; else the server for Pro; else
    /// nobody, with the reason. Preview mode always stitches.
    @MainActor static func pick(isPro: Bool, checker: CheckerKind = AppSettings.checker,
                                onDevice: any IdeaMixer = OnDeviceMixer(),
                                server: @autoclosure () -> any IdeaMixer = RemoteMixer(client: LabClient(baseURL: AppSettings.serverURL))) -> (any IdeaMixer)? {
        if checker == .preview { return PreviewMixer() }
        if onDevice.availability.isAvailable { return onDevice }
        if isPro { return server() }
        return nil
    }

    static let unavailableReason = "Mixing needs Apple Intelligence on this iPhone, or Pro to mix on Sparkjudge's server."
}
