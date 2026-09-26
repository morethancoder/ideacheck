import SwiftData
import SwiftUI

/// Home: one big orb. Tap, speak, and the words type out as you go; stop
/// talking and the take ends, is saved at once, and opens for review.
struct DictateView: View {
    @Environment(AppState.self) private var appState
    @Environment(\.modelContext) private var context
    @Environment(\.scenePhase) private var scenePhase
    @State private var model = DictationModel()
    @State private var starts = 0
    @State private var stops = 0
    @AppStorage(SettingsKey.silenceSeconds) private var silenceSeconds = AppSettings.defaultSilence
    @AppStorage(SettingsKey.transcriber) private var engineRaw = TranscriberEngineID.appleSpeech.rawValue

    var body: some View {
        ZStack {
            background
            VStack(spacing: 0) {
                header
                    .padding(.top, 8)
                Spacer(minLength: 12)
                OrbView(level: model.level, active: model.isActive, silenceProgress: model.silenceProgress)
                    .frame(width: 320, height: 320)
                    .onTapGesture { toggle() }
                    .accessibilityAction { toggle() }
                transcript
                    .frame(maxWidth: .infinity, minHeight: 140, alignment: .top)
                    .padding(.horizontal, 24)
                Spacer(minLength: 12)
                footer
                    .padding(.horizontal, 24)
                    .padding(.bottom, 20)
            }
        }
        .sensoryFeedback(.start, trigger: starts)
        .sensoryFeedback(.stop, trigger: stops)
        .onAppear(perform: configure)
        .onChange(of: scenePhase) { _, phase in
            // Never lose a take to the app switcher: end it and save what was heard.
            if phase != .active, model.isActive { model.finish() }
        }
        .onChange(of: model.isActive) { was, now in
            if was && !now { stops += 1 }
        }
    }

    private var background: some View {
        ZStack {
            Color.sjInk.ignoresSafeArea()
            RadialGradient(colors: [(model.isActive ? Color.sjSpark : Color.sjViolet).opacity(0.18), .clear],
                           center: .center, startRadius: 20, endRadius: 420)
                .ignoresSafeArea()
                .animation(.easeInOut(duration: 0.6), value: model.isActive)
        }
    }

    private var header: some View {
        VStack(spacing: 6) {
            Text("SPARKJUDGE")
                .font(.sjLabel(.caption))
                .tracking(4)
                .foregroundStyle(Color.sjMuted)
            Text(model.isActive ? "Listening…" : "Catch it before it's gone")
                .font(.sjDisplay(.title))
                .foregroundStyle(Color.sjText)
                .multilineTextAlignment(.center)
                .contentTransition(.opacity)
                .animation(.easeInOut, value: model.isActive)
        }
        .padding(.horizontal, 24)
    }

    @ViewBuilder private var transcript: some View {
        if !model.text.isEmpty {
            ScrollViewReader { proxy in
                ScrollView {
                    Text("\(Text(model.finalized).foregroundStyle(Color.sjText))\(Text(model.volatile).foregroundStyle(Color.sjMuted))")
                        .font(.system(.title3, design: .serif))
                        .multilineTextAlignment(.center)
                        .frame(maxWidth: .infinity)
                        .id("end")
                }
                .frame(maxHeight: 180)
                .onChange(of: model.text) { proxy.scrollTo("end", anchor: .bottom) }
                .accessibilityLabel("Transcript: \(model.text)")
            }
        } else {
            Group {
                switch model.phase {
                case .preparing(let message):
                    Label(message, systemImage: "arrow.down.circle")
                case .listening:
                    Text("Say it however it comes out.")
                case .finishing:
                    Text("Saving…")
                case .failed(let message):
                    Text(message)
                case .idle:
                    Text(model.hint ?? "Tap the orb and say your idea. Pause for \(silenceSeconds.formatted(.number.precision(.fractionLength(1)))) s and it's saved.")
                }
            }
            .font(.callout)
            .foregroundStyle(Color.sjMuted)
            .multilineTextAlignment(.center)
            .padding(.top, 12)
        }
    }

    private var footer: some View {
        HStack(spacing: 12) {
            if model.isActive {
                Button(role: .cancel) { model.finish() } label: {
                    Label("Done", systemImage: "checkmark")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .tint(Color.sjSpark)
            } else {
                Button { typeInstead() } label: {
                    Label("Type instead", systemImage: "keyboard")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered)
                .controlSize(.large)
                .tint(Color.sjText)
            }
        }
        .font(.headline)
    }

    private func configure() {
        let launch = LaunchOptions.current
        if launch.demoDictation {
            model.makeTranscriber = { ScriptedTranscriber() }
        }
        model.onTake = { text in save(transcript: text) }
        if launch.autoDictate, !model.isActive {
            toggle()
        }
    }

    private func toggle() {
        if !model.isActive { starts += 1 }
        model.toggle()
    }

    /// The take is saved the moment it ends, before anything else can fail.
    private func save(transcript: String) {
        let idea = Idea(transcript: transcript)
        context.insert(idea)
        try? context.save()
        appState.reviewing = idea
    }

    private func typeInstead() {
        let idea = Idea()
        context.insert(idea)
        try? context.save()
        appState.reviewing = idea
    }
}
