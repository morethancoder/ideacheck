import SwiftUI

/// Settings → Judge → On this iPhone: which model judges a check on the
/// phone. The choices are what this iPhone has (Apple Intelligence when it is
/// on, Laya checkpoints downloaded here) and what the Hub lists right now,
/// each with its size and how much it reads; a checkpoint too small to hold
/// an idea is listed apart, not offered.
struct OnDeviceJudgeView: View {
    @Environment(OnDeviceJudges.self) private var judges
    @State private var removing: String?

    var body: some View {
        Form {
            current
            apple
            laya
            if !judges.hub.filter({ $0.problem != nil }).isEmpty {
                tooSmall
            }
            #if DEBUG
            if let dir = judges.debugDirectory {
                Section("Developer") {
                    LabeledContent("Laya from", value: dir.path).font(.caption2.monospaced())
                }
            }
            #endif
        }
        .scrollContentBackground(.hidden)
        .background(Color.sjInk)
        .navigationTitle("On this iPhone")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            judges.refreshDevice()
            if judges.hubState != .loaded { await judges.refreshHub() }
        }
        .refreshable {
            judges.refreshDevice()
            await judges.refreshHub()
        }
        .confirmationDialog("Remove this model?", isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }),
                            titleVisibility: .visible) {
            Button("Remove", role: .destructive) {
                if let id = removing { judges.remove(id) }
                removing = nil
            }
        } message: {
            Text("It frees the space it takes. You can download it again at any time.")
        }
    }

    private var current: some View {
        Section {
            HStack(spacing: 12) {
                Image(systemName: judges.judge == nil ? "exclamationmark.circle" : "checkmark.seal.fill")
                    .foregroundStyle(judges.judge == nil ? Verdict.park.color : Verdict.build.color)
                    .font(.title2)
                VStack(alignment: .leading, spacing: 2) {
                    Text(judges.judge.map { "\($0.title) judges your checks" } ?? "No judge on this iPhone yet").font(.headline)
                    Text(Self.summary(judges.judge, writer: judges.writerAvailable))
                        .font(.caption).foregroundStyle(Color.sjMuted)
                }
            }
            .accessibilityElement(children: .combine)
        }
    }

    static func summary(_ judge: OnDeviceJudge?, writer: Bool) -> String {
        let summary = writer ? " Apple Intelligence reads the idea first and writes the summary." : " There is no summary without Apple Intelligence."
        switch judge {
        case .laya: return "A model trained for exactly these questions: a fraction of a second a question, offline." + summary
        case .apple: return "Apple's on-device model: nothing to download, about a minute a check." + summary
        case nil: return "Download Laya below, or turn on Apple Intelligence, to check ideas on the phone."
        }
    }

    // MARK: Apple Intelligence

    private var apple: some View {
        Section {
            ChoiceRow(title: "Apple Intelligence", detail: judges.appleProblem.map { "Unavailable: \($0)." }
                        ?? "Apple's on-device model. Nothing to download; about a minute a check.",
                      selected: judges.judge == .apple, enabled: judges.appleProblem == nil) {
                judges.choose(.apple)
            }
        } footer: {
            Text("Apple Intelligence also reads your idea for stated facts and writes the summary, whichever model judges.")
        }
    }

    // MARK: Laya

    /// Checkpoints that can run a check: the Hub's, plus any on this iPhone
    /// the Hub no longer lists (or that it could not be asked about).
    private var layaRows: [OnDeviceJudges.Listing] {
        var rows = judges.hub.filter { $0.problem == nil }
        for c in judges.local where c.problem == nil && !rows.contains(where: { $0.id == c.id }) {
            rows.append(OnDeviceJudges.Listing(id: c.id))
        }
        return rows.sorted { ($0.isTypedDecisions ? 0 : 1, $0.id) < ($1.isTypedDecisions ? 0 : 1, $1.id) }
    }

    private var laya: some View {
        Section {
            switch judges.hubState {
            case .loading where judges.hub.isEmpty:
                HStack {
                    ProgressView().controlSize(.small)
                    Text("Asking Hugging Face for the models…").font(.callout).foregroundStyle(Color.sjMuted)
                }
            case .failed(let why) where judges.hub.isEmpty:
                VStack(alignment: .leading, spacing: 6) {
                    Text(why).font(.callout).foregroundStyle(Color.sjMuted)
                    Button("Try again") { Task { await judges.refreshHub() } }
                }
            default:
                EmptyView()
            }
            ForEach(layaRows) { listing in
                LayaRow(listing: listing, removing: $removing)
            }
        } header: {
            Text("Laya")
        } footer: {
            Text("Laya answers each question in one pass, with calibrated odds. The list is Hugging Face's, as it is now. A download resumes where it stopped, is checked before it is used, and stays out of your backups.")
        }
    }

    private var tooSmall: some View {
        Section {
            DisclosureGroup("\(judges.hub.filter { $0.problem != nil }.count) more can't hold an idea") {
                ForEach(judges.hub.filter { $0.problem != nil }) { listing in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(listing.name).font(.subheadline.monospaced())
                        Text(listing.problem ?? "").font(.caption).foregroundStyle(Color.sjMuted)
                    }
                    .padding(.vertical, 2)
                }
            }
            .font(.callout)
            .tint(Color.sjMuted)
        } footer: {
            Text("Exports for the Neural Engine read a few dozen words at a time: made for short questions, not for an idea and a rubric.")
        }
    }
}

/// One Laya checkpoint: choose it when it is here, else download it, with a
/// progress bar that pauses and resumes.
struct LayaRow: View {
    let listing: OnDeviceJudges.Listing
    @Binding var removing: String?
    @Environment(OnDeviceJudges.self) private var judges

    private var downloaded: Bool { judges.isDownloaded(listing.id) }
    private var selected: Bool {
        if case .laya(let id, _) = judges.judge { return id == listing.id }
        return false
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: selected ? "largecircle.fill.circle" : "circle")
                    .foregroundStyle(selected ? Color.sjSpark : Color.sjMuted)
                    .font(.title3)
                    .opacity(downloaded ? 1 : 0.35)
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 6) {
                        Text(listing.name).font(.headline.monospaced()).lineLimit(1).minimumScaleFactor(0.8)
                        if listing.isTypedDecisions {
                            Text("Recommended").font(.sjLabel(.caption2)).foregroundStyle(Verdict.build.color)
                        }
                    }
                    Text(capacity).font(.caption).foregroundStyle(Color.sjMuted)
                }
            }
            .contentShape(.rect)
            .onTapGesture { if downloaded { judges.choose(.laya(listing.id)) } }
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(selected ? [.isButton, .isSelected] : .isButton)
            LayaDownloadControl(listing: listing, removing: $removing)
                .padding(.leading, 36)
        }
        .padding(.vertical, 4)
    }

    private var capacity: String {
        var parts: [String] = []
        if let bytes = listing.bytes { parts.append(OnDeviceJudges.size(bytes)) }
        if let n = listing.maxLength { parts.append("reads \(n) tokens") }
        if let k = listing.maxOptions { parts.append("up to \(k) answers") }
        if listing.id.contains("multilingual") { parts.append("many languages") }
        return parts.isEmpty ? listing.id : parts.joined(separator: " · ")
    }
}

/// Download, pause, resume, remove — and the progress bar between.
struct LayaDownloadControl: View {
    let listing: OnDeviceJudges.Listing
    @Binding var removing: String?
    @Environment(OnDeviceJudges.self) private var judges

    var body: some View {
        switch judges.downloads[listing.id] {
        case .running(let done, let total):
            VStack(alignment: .leading, spacing: 6) {
                ProgressView(value: Double(done), total: Double(max(total, 1))).tint(Color.sjSpark)
                HStack {
                    Text("\(OnDeviceJudges.size(done)) of \(OnDeviceJudges.size(total))")
                        .font(.caption.monospacedDigit()).foregroundStyle(Color.sjMuted)
                    Spacer()
                    Button("Pause", systemImage: "pause.fill") { judges.pause(listing.id) }
                        .font(.caption.weight(.semibold)).buttonStyle(.bordered).controlSize(.small)
                }
            }
        case .preparing:
            HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text("Getting it ready…").font(.caption).foregroundStyle(Color.sjMuted)
            }
        case .failed(let why):
            VStack(alignment: .leading, spacing: 6) {
                Text(why).font(.caption).foregroundStyle(Verdict.park.color)
                Button("Try again") { judges.download(listing.id) }
                    .font(.caption.weight(.semibold)).buttonStyle(.bordered).controlSize(.small)
            }
        case nil:
            if judges.isDownloaded(listing.id) {
                HStack {
                    Label(judges.onDisk[listing.id].map { "On this iPhone · \(OnDeviceJudges.size($0))" } ?? "Read from -sjLayaDir",
                          systemImage: "checkmark.circle.fill")
                        .font(.caption).foregroundStyle(Verdict.build.color)
                    Spacer()
                    if judges.debugDirectory == nil || judges.onDisk[listing.id] != nil {
                        Button("Remove", role: .destructive) { removing = listing.id }
                            .font(.caption.weight(.semibold)).buttonStyle(.bordered).controlSize(.small)
                    }
                }
            } else if let bytes = listing.bytes {
                let have = judges.onDisk[listing.id] ?? 0
                HStack {
                    if have > 0 {
                        Text("\(OnDeviceJudges.size(have)) of \(OnDeviceJudges.size(bytes)) so far")
                            .font(.caption.monospacedDigit()).foregroundStyle(Color.sjMuted)
                    }
                    Spacer()
                    Button(have > 0 ? "Resume" : "Download · \(OnDeviceJudges.size(bytes))",
                           systemImage: have > 0 ? "arrow.down.circle" : "arrow.down.circle.fill") {
                        judges.download(listing.id)
                    }
                    .font(.caption.weight(.semibold))
                    .buttonStyle(.borderedProminent)
                    .tint(Color.sjSpark)
                    .foregroundStyle(.black)
                    .controlSize(.small)
                }
            }
        }
    }
}

/// A radio row with a title and one line of detail.
struct ChoiceRow: View {
    var title: String
    var detail: String
    var selected: Bool
    var enabled: Bool
    var choose: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: selected ? "largecircle.fill.circle" : "circle")
                .foregroundStyle(selected ? Color.sjSpark : Color.sjMuted)
                .font(.title3)
            VStack(alignment: .leading, spacing: 3) {
                Text(title).font(.headline)
                Text(detail).font(.caption).foregroundStyle(Color.sjMuted)
            }
        }
        .opacity(enabled ? 1 : 0.55)
        .contentShape(.rect)
        .onTapGesture { if enabled { choose() } }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(selected ? [.isButton, .isSelected] : .isButton)
    }
}
