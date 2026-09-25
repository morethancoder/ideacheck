import SwiftUI

struct SettingsView: View {
    @AppStorage(SettingsKey.transcriber) private var engineRaw = TranscriberEngineID.appleSpeech.rawValue
    @AppStorage(SettingsKey.locale) private var localeID = ""
    @AppStorage(SettingsKey.silenceSeconds) private var silence = AppSettings.defaultSilence
    @AppStorage(SettingsKey.vadSensitivity) private var vad = 1
    @AppStorage(SettingsKey.autoCheck) private var autoCheck = false
    /// nil until chosen: CheckRoute then picks by plan.
    @AppStorage(SettingsKey.checker) private var checkerRaw: String?
    @AppStorage(SettingsKey.serverURL) private var serverURL = ""
    @Environment(Entitlements.self) private var entitlements
    @Environment(OnDeviceJudges.self) private var judges
    @State private var path = NavigationPath()
    @Environment(\.ideaSuggester) private var writer
    @State private var catalog = TranscriberCatalog()
    @State private var connection: ConnectionState = .unknown

    enum ConnectionState: Equatable {
        case unknown, testing, ok, failed(String)
    }

    private var engine: TranscriberEngineID { TranscriberEngineID(rawValue: engineRaw) ?? .appleSpeech }
    private var checker: CheckerKind {
        CheckRoute.resolve(stored: checkerRaw.flatMap(CheckerKind.init(rawValue:)), isPro: entitlements.isPro)
    }

    /// Settings' inner pages, for `-sjOpen judge`.
    enum Page: Hashable { case onDeviceJudge }

    var body: some View {
        NavigationStack(path: $path) {
            Form {
                judge
                plan
                transcription
                capture
                review
                cardStyles
                #if DEBUG
                developer
                #endif
                about
            }
            .scrollContentBackground(.hidden)
            .background(Color.sjInk)
            .navigationTitle("Settings")
            .navigationDestination(for: Page.self) { page in
                switch page {
                case .onDeviceJudge: OnDeviceJudgeView()
                }
            }
            .refreshable { await entitlements.refreshPlan() }
            .task { await entitlements.refreshPlan() }
            .task {
                judges.refreshDevice()
                if LaunchOptions.current.open == "judge", path.isEmpty { path.append(Page.onDeviceJudge) }
                #if DEBUG
                if let id = UserDefaults.standard.string(forKey: "sjLayaDownload"), !id.isEmpty {
                    await judges.refreshHub()
                    judges.download(id)
                }
                #endif
            }
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

    // MARK: - Judge and plan

    /// Who checks an idea: the model on this iPhone (free, private) or
    /// Sparkjudge Cloud (Pro: Jev judging, with web research).
    private var judge: some View {
        Section {
            JudgeRow(title: "On this iPhone", badge: "Free · private", symbol: "iphone",
                     detail: onDeviceDetail,
                     selected: checker == .onDevice, locked: false, available: true) {
                checkerRaw = CheckerKind.onDevice.rawValue
            }
            NavigationLink(value: Page.onDeviceJudge) {
                LabeledContent {
                    Text(judges.judge?.title ?? "None yet")
                        .foregroundStyle(judges.judge == nil ? Verdict.park.color : Color.sjMuted)
                } label: {
                    Label("Model on this iPhone", systemImage: "cpu")
                }
            }
            JudgeRow(title: "Sparkjudge Cloud", badge: entitlements.isPro ? "Pro" : "Pro · try free", symbol: "cloud",
                     detail: "Jev, a judge built for these questions, scores the idea after searching the web for competitors and demand. More accurate, with sources.",
                     selected: checker == .remote, locked: !entitlements.isPro, available: true) {
                checkerRaw = CheckerKind.remote.rawValue
            }
        } header: {
            Text("Judge")
        } footer: {
            if checker == .remote, !entitlements.isPro, let plan = entitlements.plan {
                Text("Free accounts get \(plan.limit) hosted checks a month to try Sparkjudge Cloud; \(plan.remaining) left, renewing \(plan.resetsAt.formatted(.dateTime.month(.wide).day())).")
            } else if checker == .preview {
                Text(CheckerKind.preview.detail)
            }
        }
    }

    private var onDeviceDetail: String {
        switch judges.judge {
        case .laya: "Laya judges, in seconds, and nothing leaves your phone. No web research."
        case .apple: "Apple Intelligence judges, about a minute a check, and nothing leaves your phone. No web research."
        case nil: "Nothing leaves your phone. This iPhone needs a judge first: download Laya below."
        }
    }

    private var plan: some View {
        Section {
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(entitlements.isPro ? "Sparkjudge Pro" : "Free").font(.headline)
                    if let plan = entitlements.plan {
                        Text(plan.usageLine).font(.subheadline).foregroundStyle(Color.sjMuted)
                    } else if entitlements.loadingPlan {
                        Text("Asking Sparkjudge Cloud…").font(.subheadline).foregroundStyle(Color.sjMuted)
                    } else if let problem = entitlements.planProblem {
                        Text(problem).font(.subheadline).foregroundStyle(Color.sjMuted)
                    }
                }
                Spacer()
                if let plan = entitlements.plan {
                    UsageRing(used: plan.used, limit: plan.limit)
                }
            }
            .accessibilityElement(children: .combine)
            if entitlements.isPro {
                if let until = entitlements.plan?.proUntil {
                    LabeledContent("Renews or ends", value: until.formatted(date: .abbreviated, time: .omitted))
                }
                Link("Manage subscription", destination: URL(string: "https://apps.apple.com/account/subscriptions")!)
            } else {
                Button {
                    entitlements.paywall = .browse
                } label: {
                    Label("See what Pro adds", systemImage: "sparkles")
                }
                .tint(Color.sjSpark)
            }
            Button("Restore purchases") { Task { await entitlements.restore() } }
                .disabled(entitlements.restoring)
            if let notice = entitlements.notice {
                Text(notice).font(.footnote).foregroundStyle(Color.sjMuted)
            }
        } header: {
            Text("Plan")
        } footer: {
            if let plan = entitlements.plan {
                Text("Hosted checks reset on \(plan.resetsAt.formatted(.dateTime.month(.wide).day())). A check that ends without a score is given back.")
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
                        .onTapGesture { if locked { entitlements.paywall = .styles } }
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel("\(family.name) style\(locked ? ", Pro, locked" : "")")
                        .accessibilityAddTraits(locked ? .isButton : [])
                }
            }
            .padding(.vertical, 6)
        } header: {
            Text("Card styles")
        } footer: {
            Text(entitlements.isPro
                 ? "Every checked idea gets its own background, rolled from the idea itself, from all six families."
                 : "Every checked idea gets its own background, rolled from the idea itself. Free ideas draw from three families; Pro unlocks all six.")
        }
    }

    #if DEBUG
    /// Debug builds only: the server, the offline preview and a Pro switch.
    private var developer: some View {
        @Bindable var entitlements = entitlements
        return Section {
            Picker("Check with", selection: $checkerRaw) {
                Text("By plan").tag(String?.none)
                Text(CheckerKind.onDevice.title).tag(String?.some(CheckerKind.onDevice.rawValue))
                Text("Sparkjudge Cloud").tag(String?.some(CheckerKind.remote.rawValue))
                Text(CheckerKind.preview.title).tag(String?.some(CheckerKind.preview.rawValue))
            }
            TextField(AppSettings.hostedURL.absoluteString, text: $serverURL)
                .keyboardType(.URL)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .font(.system(.body, design: .monospaced))
                .onChange(of: serverURL) { connection = .unknown }
                .onSubmit { Task { await entitlements.refreshPlan() } }
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
            LabeledContent("Signing", value: HostedAuth.resolve(baseURL: AppSettings.serverURL) == .appAttest ? "App Attest" : "user id only (attest off)")
            LabeledContent("User id") {
                Text(entitlements.userID).font(.caption2.monospaced()).textSelection(.enabled)
            }
            Toggle("Pro preview", isOn: $entitlements.previewPro)
                .tint(Color.sjViolet)
        } header: {
            Text("Developer")
        } footer: {
            VStack(alignment: .leading, spacing: 4) {
                Text("Empty = this build's server (\(AppSettings.hostedURL.absoluteString)); run `make api` on the Mac. `ideacheck serve` works too. From a phone, use the Mac's address on your network. Pro preview unlocks card styles on this phone only.")
                if case .failed(let why) = connection { Text(why).foregroundStyle(Verdict.kill.color) }
            }
        }
    }

    private func test() async {
        connection = .testing
        switch await SparkjudgeAPI.shared(baseURL: AppSettings.serverURL).health() {
        case .success: connection = .ok
        case .failure(let error): connection = .failed(error.localizedDescription)
        }
    }
    #endif

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

/// One judge in the Judge section: what it is, whether it is Pro, and
/// whether this build can run it.
struct JudgeRow: View {
    var title: String
    var badge: String
    var symbol: String
    var detail: String
    var selected: Bool
    var locked: Bool
    var available: Bool
    var choose: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: selected ? "largecircle.fill.circle" : "circle")
                .foregroundStyle(selected ? Color.sjSpark : Color.sjMuted)
                .font(.title3)
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    Image(systemName: symbol).foregroundStyle(Color.sjMuted)
                    Text(title).font(.headline)
                    Spacer()
                    if locked { Image(systemName: "lock.fill").font(.caption).foregroundStyle(Color.sjViolet) }
                    Text(badge).font(.sjLabel(.caption2)).foregroundStyle(locked ? Color.sjViolet : Verdict.build.color)
                }
                Text(detail).font(.caption).foregroundStyle(Color.sjMuted)
            }
        }
        .opacity(available ? 1 : 0.55)
        .contentShape(.rect)
        .onTapGesture { if available { choose() } }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(selected ? [.isButton, .isSelected] : .isButton)
    }
}

/// Hosted checks used this month, as a ring.
struct UsageRing: View {
    var used: Int
    var limit: Int

    private var fraction: Double { limit == 0 ? 1 : min(1, Double(used) / Double(limit)) }

    var body: some View {
        ZStack {
            Circle().stroke(Color.sjRaised, lineWidth: 5)
            Circle().trim(from: 0, to: fraction)
                .stroke(fraction >= 1 ? Verdict.park.color : Color.sjSpark, style: StrokeStyle(lineWidth: 5, lineCap: .round))
                .rotationEffect(.degrees(-90))
            Text("\(max(0, limit - used))").font(.system(.caption, design: .rounded, weight: .bold))
        }
        .frame(width: 40, height: 40)
        .accessibilityHidden(true)
    }
}
