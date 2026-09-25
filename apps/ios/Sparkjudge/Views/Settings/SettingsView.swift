import SwiftUI

struct SettingsView: View {
    @AppStorage(SettingsKey.transcriber) private var engineRaw = TranscriberEngineID.appleSpeech.rawValue
    @AppStorage(SettingsKey.locale) private var localeID = ""
    @AppStorage(SettingsKey.silenceSeconds) private var silence = AppSettings.defaultSilence
    @AppStorage(SettingsKey.vadSensitivity) private var vad = 1
    @AppStorage(SettingsKey.autoCheck) private var autoCheck = false
    @AppStorage(SettingsKey.checker) private var checkerRaw = CheckerKind.remote.rawValue
    @AppStorage(SettingsKey.serverURL) private var serverURL = AppSettings.defaultServerURL
    @AppStorage(SettingsKey.proPreview) private var proPreview = false
    @Environment(Entitlements.self) private var entitlements
    @Environment(\.ideaSuggester) private var writer
    @State private var catalog = TranscriberCatalog()
    @State private var connection: ConnectionState = .unknown

    enum ConnectionState: Equatable {
        case unknown, testing, ok, failed(String)
    }

    private var engine: TranscriberEngineID { TranscriberEngineID(rawValue: engineRaw) ?? .appleSpeech }

    var body: some View {
        NavigationStack {
            Form {
                transcription
                capture
                review
                checking
                cardStyles
                about
            }
            .scrollContentBackground(.hidden)
            .background(Color.sjInk)
            .navigationTitle("Settings")
            .task(id: "\(engineRaw)|\(localeID)") {
                await catalog.refresh(engine: engine, localeIdentifier: localeID.isEmpty ? nil : localeID)
                // The choice must be one this device can run: fall back to the first that is.
                if catalog.options.first(where: { $0.engine == engine })?.availability.isSelectable == false,
                   let usable = catalog.options.first(where: { $0.availability.isSelectable }) {
                    engineRaw = usable.engine.rawValue
                }
            }
        }
    }

    // MARK: - Transcription

    private var transcription: some View {
        Section {
            ForEach(catalog.options) { option in
                EngineRow(option: option, selected: option.engine == engine)
                    .contentShape(.rect)
                    .onTapGesture {
                        guard option.availability.isSelectable else { return }
                        engineRaw = option.engine.rawValue
                    }
                    .disabled(!option.availability.isSelectable)
                    .accessibilityAddTraits(option.engine == engine ? [.isButton, .isSelected] : .isButton)
            }
            Picker("Language", selection: $localeID) {
                Text("Same as the phone").tag("")
                ForEach(catalog.locales) { l in
                    Text(l.installed ? "\(l.name) ✓" : l.name).tag(l.identifier)
                }
            }
            .disabled(catalog.locales.isEmpty)
        } header: {
            Text("Transcription")
        } footer: {
            Text(catalog.locales.isEmpty && catalog.loaded
                 ? "This device reports no on-device speech models for the chosen engine (the simulator has none). You can still type ideas."
                 : "Engines and languages are the ones this device says it supports. ✓ = already downloaded; others download the first time you dictate.")
        }
    }

    private var capture: some View {
        Section {
            VStack(alignment: .leading) {
                LabeledContent("End a take after", value: "\(silence.formatted(.number.precision(.fractionLength(1)))) s of silence")
                Slider(value: $silence, in: AppSettings.silenceRange, step: 0.2) {
                    Text("Silence before a take ends")
                } minimumValueLabel: {
                    Text("0.8").font(.caption)
                } maximumValueLabel: {
                    Text("6").font(.caption)
                }
                .tint(Color.sjSpark)
            }
            Picker("Voice detection", selection: $vad) {
                Text("Relaxed").tag(0)
                Text("Balanced").tag(1)
                Text("Eager").tag(2)
            }
        } header: {
            Text("Dictation")
        } footer: {
            Text("A take ends once you've spoken and then stayed quiet this long. Voice detection is Apple's SpeechDetector: eager hears quieter speech, relaxed ignores more background noise.")
        }
    }

    private var review: some View {
        Section {
            Toggle("Check automatically after review", isOn: $autoCheck)
                .tint(Color.sjSpark)
            LabeledContent("Suggestions") {
                switch writer.availability {
                case .available: Text("Apple Intelligence").foregroundStyle(Verdict.build.color)
                case .unavailable: Text("Off").foregroundStyle(Color.sjMuted)
                }
            }
        } header: {
            Text("Review")
        } footer: {
            if case .unavailable(let why) = writer.availability {
                Text("Title, kind and details are suggested on this device by Apple's foundation model. \(why)")
            } else {
                Text("Title, kind and details are suggested on this device by Apple's foundation model. Nothing leaves the phone.")
            }
        }
    }

    private var checking: some View {
        Section {
            Picker("Check with", selection: $checkerRaw) {
                ForEach(CheckerKind.allCases) { k in
                    Text(k == .onDevice ? "\(k.title) (later)" : k.title)
                        .tag(k.rawValue)
                        .selectionDisabled(k == .onDevice && !OnDeviceChecker.isAvailable)
                }
            }
            if checkerRaw == CheckerKind.remote.rawValue {
                TextField("Server URL", text: $serverURL)
                    .keyboardType(.URL)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .font(.system(.body, design: .monospaced))
                    .onChange(of: serverURL) { connection = .unknown }
                HStack {
                    Button("Test connection") { Task { await test() } }
                        .disabled(connection == .testing)
                    Spacer()
                    switch connection {
                    case .unknown: EmptyView()
                    case .testing: ProgressView().controlSize(.small)
                    case .ok: Label("Reachable", systemImage: "checkmark.circle.fill").foregroundStyle(Verdict.build.color)
                    case .failed: Label("Not reachable", systemImage: "xmark.circle.fill").foregroundStyle(Verdict.kill.color)
                    }
                }
            }
        } header: {
            Text("Checking")
        } footer: {
            VStack(alignment: .leading, spacing: 4) {
                Text((CheckerKind(rawValue: checkerRaw) ?? .remote).detail)
                if checkerRaw == CheckerKind.remote.rawValue {
                    Text("Run `ideacheck serve` on your Mac. From a phone, use the Mac's address on your network instead of 127.0.0.1.")
                }
                if case .failed(let why) = connection { Text(why).foregroundStyle(Verdict.kill.color) }
            }
        }
    }

    private var cardStyles: some View {
        Section {
            LazyVGrid(columns: [GridItem(.adaptive(minimum: 92), spacing: 10)], spacing: 10) {
                ForEach(CardFamily.allCases) { family in
                    let locked = !entitlements.allows(family)
                    CardBackground(style: CardStyle.make(seed: 0x5EED &+ UInt64(family.rawValue) &* 104_729, allowed: [family]))
                        .frame(height: 110)
                        .clipShape(.rect(cornerRadius: 14, style: .continuous))
                        .overlay(alignment: .bottomLeading) {
                            Text(family.name).font(.caption.weight(.bold)).foregroundStyle(.white).padding(8)
                        }
                        .overlay(alignment: .topTrailing) {
                            if locked {
                                Label("Pro", systemImage: "lock.fill")
                                    .font(.caption2.weight(.heavy))
                                    .padding(.horizontal, 6).padding(.vertical, 3)
                                    .background(.ultraThinMaterial, in: Capsule())
                                    .foregroundStyle(.white)
                                    .padding(6)
                            }
                        }
                        .saturation(locked ? 0.3 : 1)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel("\(family.name) style\(locked ? ", Pro, locked" : "")")
                }
            }
            .padding(.vertical, 6)
            Toggle("Pro preview (developer)", isOn: $proPreview)
                .tint(Color.sjViolet)
                .onChange(of: proPreview) { _, on in entitlements.isPro = on }
        } header: {
            Text("Card styles")
        } footer: {
            Text("Every checked idea gets its own background, rolled from the idea itself. Free ideas draw from three families; Pro unlocks all six. Purchases aren't wired up yet.")
        }
    }

    private var about: some View {
        Section {
            LabeledContent("Version", value: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "–")
            NavigationLink("What a check is — and isn't") {
                ScrollView {
                    Text("A check asks a judge model a set of narrow, typed questions about your idea at once, weighs the answers, and gives a score per dimension, a verdict — build, explore, park or kill — and what's missing. It is not a success predictor: it tells you where an idea is thin while it's still cheap to change.")
                        .font(.system(.body, design: .serif))
                        .padding(20)
                }
                .background(Color.sjInk)
                .navigationTitle("About checks")
            }
        }
    }

    private func test() async {
        connection = .testing
        guard let url = URL(string: serverURL.trimmingCharacters(in: .whitespaces)), url.scheme != nil else {
            connection = .failed("That isn't a URL.")
            return
        }
        switch await RemoteChecker(baseURL: url).health() {
        case .success: connection = .ok
        case .failure(let error): connection = .failed(error.localizedDescription)
        }
    }
}

/// One engine in the picker: what it is, how fast and how good, and whether
/// this device can run it.
struct EngineRow: View {
    let option: EngineOption
    let selected: Bool

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: selected ? "largecircle.fill.circle" : "circle")
                .foregroundStyle(selected ? Color.sjSpark : Color.sjMuted)
                .font(.title3)
            VStack(alignment: .leading, spacing: 4) {
                HStack {
                    Text(option.engine.name).font(.headline)
                    Spacer()
                    Text(option.availability.label)
                        .font(.sjLabel(.caption2))
                        .foregroundStyle(tint)
                }
                Text(option.engine.detail).font(.caption).foregroundStyle(Color.sjMuted)
                HStack(spacing: 14) {
                    meter("Speed", option.engine.speed, symbol: "bolt.fill")
                    meter("Quality", option.engine.quality, symbol: "star.fill")
                    Text(option.engine.hardware).font(.caption2).foregroundStyle(Color.sjMuted)
                }
                if case .unsupported(let why) = option.availability {
                    Text(why).font(.caption2).foregroundStyle(Color.sjMuted)
                }
            }
        }
        .opacity(option.availability.isSelectable ? 1 : 0.55)
        .accessibilityElement(children: .combine)
    }

    private var tint: Color {
        switch option.availability {
        case .ready: Verdict.build.color
        case .needsDownload: Verdict.explore.color
        case .unsupported: Color.sjMuted
        case .comingSoon: Color.sjViolet
        }
    }

    private func meter(_ label: String, _ value: Int, symbol: String) -> some View {
        HStack(spacing: 1) {
            ForEach(0..<3) { i in
                Image(systemName: symbol)
                    .font(.system(size: 8))
                    .foregroundStyle(i < value ? Color.sjSpark : Color.sjRaised)
            }
        }
        .accessibilityLabel("\(label) \(value) of 3")
    }
}
