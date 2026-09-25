import SwiftData
import SwiftUI

extension EnvironmentValues {
    /// The writer that suggests a title, category and fields on review.
    @Entry var ideaSuggester: any IdeaSuggester = OnDeviceWriter()
}

/// Review a saved take: fix the transcript, accept or edit the suggested title,
/// category and fields, then check it.
struct ReviewView: View {
    @Bindable var idea: Idea
    /// Called once when review ends: `true` when the person pressed Check,
    /// with the checker to use once (nil = Settings' choice).
    var onFinish: (Bool, CheckerKind?) -> Void

    @Environment(\.ideaSuggester) private var writer
    @Environment(Entitlements.self) private var entitlements
    @Environment(OnDeviceJudges.self) private var judges
    @AppStorage(SettingsKey.autoCheck) private var autoCheck = false
    @AppStorage(SettingsKey.layaOffered) private var layaOffered = false
    @State private var offer: JudgeOfferReason?
    @State private var suggesting = false
    @State private var suggestedFields: Set<String> = []
    @State private var writerNote: String?
    @State private var finished = false
    @FocusState private var focus: String?

    private let catalog = FieldCatalog.shared

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 28) {
                    savedLine
                    if let origin = idea.origin {
                        OriginLine(origin: origin)
                    }
                    titleField
                    categoryPicker
                    transcriptField
                    fieldsSection
                }
                .padding(20)
                .padding(.bottom, 100)
            }
            .scrollDismissesKeyboard(.interactively)
            .background(Color.sjInk)
            .safeAreaInset(edge: .bottom) { checkBar }
            .navigationTitle("Review")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { finish(check: false) }
                        .accessibilityHint(autoCheck ? "Saves and checks the idea" : "Saves the idea without checking")
                }
            }
        }
        .task { await suggest() }
        .onDisappear { if !finished { finish(check: false) } }
        .sheet(item: $offer) { reason in
            JudgeOfferSheet(reason: reason) { kind in
                offer = nil
                finish(check: true, with: kind)
            } close: {
                offer = nil
            }
            .presentationDetents([.large])
        }
    }

    // MARK: - Sections

    private var savedLine: some View {
        HStack(spacing: 8) {
            Image(systemName: "checkmark.seal.fill").foregroundStyle(Verdict.build.color)
            Text("Saved to your ideas").font(.sjLabel(.footnote))
            Spacer()
            if suggesting {
                ProgressView().controlSize(.small)
                Text("Reading your idea…").font(.sjLabel(.footnote)).foregroundStyle(Color.sjMuted)
            }
        }
        .foregroundStyle(Color.sjText)
        .overlay(alignment: .bottomLeading) {
            if let writerNote {
                Text(writerNote)
                    .font(.footnote)
                    .foregroundStyle(Color.sjMuted)
                    .offset(y: 22)
            }
        }
        .padding(.bottom, writerNote == nil ? 0 : 18)
    }

    private var titleField: some View {
        VStack(alignment: .leading, spacing: 6) {
            label("Name", suggested: suggestedFields.contains("title"))
            TextField("Give it a name", text: $idea.title, axis: .vertical)
                .font(.sjDisplay(.largeTitle))
                .foregroundStyle(Color.sjText)
                .focused($focus, equals: "title")
                .submitLabel(.done)
        }
    }

    private var categoryPicker: some View {
        VStack(alignment: .leading, spacing: 10) {
            label("Kind of idea", suggested: suggestedFields.contains("category"))
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    ForEach(IdeaCategory.displayOrder) { c in
                        let selected = idea.category == c
                        Button {
                            withAnimation(.snappy) {
                                idea.category = c
                                idea.categoryChosen = c != .other
                                suggestedFields.remove("category")
                            }
                        } label: {
                            Label(c == .other ? "Let the check decide" : c.badge, systemImage: c == .other ? "wand.and.stars" : c.symbol)
                                .font(.subheadline.weight(.semibold))
                                .padding(.horizontal, 14)
                                .padding(.vertical, 10)
                                .foregroundStyle(selected ? Color.black : Color.sjText)
                                .background(selected ? Color.sjSpark : Color.sjRaised, in: Capsule())
                        }
                        .buttonStyle(.plain)
                        .accessibilityAddTraits(selected ? .isSelected : [])
                    }
                }
            }
            .scrollClipDisabled()
        }
    }

    private var transcriptField: some View {
        VStack(alignment: .leading, spacing: 6) {
            label("What you said", suggested: false)
            TextField("Your idea, in your words", text: $idea.transcript, axis: .vertical)
                .font(.system(.body, design: .serif))
                .lineLimit(3...12)
                .padding(14)
                .background(Color.sjSurface, in: .rect(cornerRadius: 16))
                .focused($focus, equals: "transcript")
        }
    }

    private var fieldsSection: some View {
        VStack(alignment: .leading, spacing: 18) {
            SectionTitle(title: "The details", symbol: "list.bullet.rectangle", trailing: "optional")
            Text("Anything you leave empty is what a check will ask about.")
                .font(.footnote)
                .foregroundStyle(Color.sjMuted)
            ForEach(catalog.reviewFields) { spec in
                VStack(alignment: .leading, spacing: 6) {
                    label(spec.label, suggested: suggestedFields.contains(spec.name))
                    TextField(spec.example.map { "e.g. \($0)" } ?? spec.label, text: binding(for: spec.name), axis: .vertical)
                        .lineLimit(1...5)
                        .padding(12)
                        .background(Color.sjSurface, in: .rect(cornerRadius: 14))
                        .focused($focus, equals: spec.name)
                    Text(spec.description)
                        .font(.caption)
                        .foregroundStyle(Color.sjMuted)
                }
            }
        }
    }

    private var checkBar: some View {
        VStack(spacing: 6) {
            Button { checkTapped() } label: {
                Label("Check this idea", systemImage: "sparkles")
                    .font(.headline)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 6)
            }
            .buttonStyle(.borderedProminent)
            .buttonBorderShape(.capsule)
            .controlSize(.large)
            .tint(Color.sjSpark)
            .foregroundStyle(.black)
            .disabled(idea.transcript.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && idea.title.isEmpty)
            HostedAllowanceNote()
            if autoCheck {
                Text("Done checks it too — “Check automatically after review” is on.")
                    .font(.caption2)
                    .foregroundStyle(Color.sjMuted)
            }
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 12)
        .background(.bar)
    }

    private func label(_ text: String, suggested: Bool) -> some View {
        HStack(spacing: 6) {
            Text(text).font(.sjLabel(.caption)).textCase(.uppercase).foregroundStyle(Color.sjMuted)
            if suggested {
                Image(systemName: "sparkle")
                    .font(.caption2)
                    .foregroundStyle(Color.sjViolet)
                    .accessibilityLabel("suggested")
            }
        }
    }

    private func binding(for name: String) -> Binding<String> {
        Binding(
            get: { idea.fields[name] ?? "" },
            set: { value in
                var f = idea.fields
                f[name] = value
                idea.fields = f
                suggestedFields.remove(name)
            }
        )
    }

    // MARK: - Actions

    /// Suggestions fill only what is still empty, once per idea.
    private func suggest() async {
        guard !idea.suggested, !idea.transcript.isEmpty else { return }
        guard writer.availability.isAvailable else {
            if case .unavailable(let why) = writer.availability { writerNote = why }
            return
        }
        suggesting = true
        defer { suggesting = false }
        do {
            let s = try await writer.suggest(transcript: idea.transcript)
            withAnimation(.smooth) {
                if idea.title.trimmingCharacters(in: .whitespaces).isEmpty, !s.title.isEmpty {
                    idea.title = s.title
                    suggestedFields.insert("title")
                }
                if !idea.categoryChosen, s.category != .other {
                    idea.category = s.category
                    suggestedFields.insert("category")
                }
                var f = idea.fields
                for (k, v) in s.fields where (f[k] ?? "").isEmpty {
                    f[k] = v
                    suggestedFields.insert(k)
                }
                idea.fields = f
            }
            idea.suggested = true
        } catch {
            writerNote = "No suggestions this time — fill in what you like."
        }
    }

    /// Checks at once, unless the check would run on this iPhone and it
    /// has no judge (then the sheet says how to get one), or Apple
    /// Intelligence would judge and Laya has not been offered yet (once).
    private func checkTapped() {
        if let reason = Self.offer(route: AppSettings.checker(isPro: entitlements.isPro), judge: judges.judge,
                                   hasLaya: judges.local.contains { $0.problem == nil }, offered: layaOffered) {
            if reason == .faster { layaOffered = true }
            offer = reason
            return
        }
        finish(check: true)
    }

    static func offer(route: CheckerKind, judge: OnDeviceJudge?, hasLaya: Bool, offered: Bool) -> JudgeOfferReason? {
        guard route == .onDevice else { return nil }
        if judge == nil { return .noJudge }
        if judge == .apple, !hasLaya, !offered { return .faster }
        return nil
    }

    private func finish(check: Bool, with kind: CheckerKind? = nil) {
        guard !finished else { return }
        finished = true
        onFinish(check, kind)
    }
}
