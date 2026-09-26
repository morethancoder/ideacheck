import SwiftUI

/// Why Review's Check opened this sheet instead of checking at once.
enum JudgeOfferReason: String, Identifiable {
    /// This iPhone has no judge: Laya is not downloaded and Apple
    /// Intelligence is off (or this device cannot run it).
    case noJudge
    /// Apple Intelligence will judge; Laya would be faster. Offered once.
    case faster

    var id: String { rawValue }
}

/// How to get an on-device judge, from Review's Check: download Laya, or
/// check this idea with Sparkjudge Cloud (the free monthly checks, or Pro).
/// "Faster" is the one-time offer made when Apple Intelligence would judge.
struct JudgeOfferSheet: View {
    let reason: JudgeOfferReason
    /// Check now: nil = on this iPhone (Settings' choice), or this checker once.
    var check: (CheckerKind?) -> Void
    var close: () -> Void

    @Environment(OnDeviceJudges.self) private var judges
    @Environment(Entitlements.self) private var entitlements

    private var listing: OnDeviceJudges.Listing? { judges.recommended }
    private var ready: Bool { judges.judge != nil }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                Image(systemName: reason == .faster ? "hare.fill" : "iphone.gen3")
                    .font(.system(size: 40))
                    .foregroundStyle(Color.sjSpark)
                    .padding(.top, 8)
                Text(reason == .faster ? "Check faster with Laya" : "Check on this iPhone")
                    .font(.sjDisplay(.title))
                Text(message).font(.body).foregroundStyle(Color.sjMuted).fixedSize(horizontal: false, vertical: true)
                download
                if reason == .noJudge {
                    cloud
                    if let problem = judges.appleProblem, problem.contains("turned off") || problem.contains("downloading") {
                        Label("Apple Intelligence can check too, once it is on: \(problem).", systemImage: "sparkles")
                            .font(.footnote).foregroundStyle(Color.sjMuted)
                    }
                }
            }
            .padding(24)
        }
        .background(Color.sjInk)
        .safeAreaInset(edge: .bottom) { actions }
        .task { if judges.hubState != .loaded { await judges.refreshHub() } }
    }

    private var message: String {
        let size = listing?.bytes.map(OnDeviceJudges.size) ?? "about 850 MB"
        switch reason {
        case .noJudge:
            return "Your idea is saved. To check it here, offline and private, this iPhone needs a judge: Laya is a small model trained for exactly these questions. It downloads once (\(size)) and then judges in seconds."
        case .faster:
            return "Apple Intelligence will check this idea now, which takes a minute or so. Laya, a small model trained for exactly these questions, judges in seconds after one download of \(size). Best on Wi-Fi."
        }
    }

    @ViewBuilder private var download: some View {
        if let listing {
            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    Text(listing.name).font(.subheadline.monospaced())
                    Spacer()
                    if let n = listing.maxLength { Text("reads \(n) tokens").font(.caption).foregroundStyle(Color.sjMuted) }
                }
                LayaDownloadControl(listing: listing, removing: .constant(nil))
            }
            .padding(14)
            .background(Color.sjSurface, in: .rect(cornerRadius: 16))
        } else {
            switch judges.hubState {
            case .failed(let why):
                VStack(alignment: .leading, spacing: 6) {
                    Text(why).font(.footnote).foregroundStyle(Color.sjMuted)
                    Button("Try again") { Task { await judges.refreshHub() } }.font(.footnote)
                }
            default:
                HStack {
                    ProgressView().controlSize(.small)
                    Text("Looking up Laya…").font(.footnote).foregroundStyle(Color.sjMuted)
                }
            }
        }
    }

    private var cloud: some View {
        VStack(alignment: .leading, spacing: 4) {
            Label("Or check this one with Sparkjudge Cloud", systemImage: "cloud").font(.subheadline.weight(.semibold))
            Text(cloudLine).font(.footnote).foregroundStyle(Color.sjMuted)
        }
    }

    private var cloudLine: String {
        if entitlements.isPro { return "Jev judges it after reading the web. Part of your Pro plan." }
        if let plan = entitlements.plan {
            return plan.isUsedUp
                ? "This month's free hosted checks are used; Pro gives you 100 a month."
                : "Jev judges it after reading the web. \(plan.remaining) of \(plan.limit) free hosted checks left this month."
        }
        return "Jev judges it after reading the web. Free accounts get a few hosted checks a month."
    }

    private var actions: some View {
        VStack(spacing: 10) {
            switch reason {
            case .noJudge:
                Button { check(nil) } label: {
                    Label(ready ? "Check on this iPhone" : "Check once Laya is here", systemImage: "sparkles")
                        .font(.headline).frame(maxWidth: .infinity).padding(.vertical, 6)
                }
                .buttonStyle(.borderedProminent).tint(Color.sjSpark).foregroundStyle(.black)
                .disabled(!ready)
                Button { check(.remote) } label: {
                    Text("Check with Sparkjudge Cloud").frame(maxWidth: .infinity).padding(.vertical, 4)
                }
                .buttonStyle(.bordered).tint(Color.sjText)
                Button("Not now") { close() }.font(.callout).tint(Color.sjMuted)
            case .faster:
                Button {
                    if let listing, judges.downloads[listing.id] == nil, !judges.isDownloaded(listing.id) {
                        judges.download(listing.id)
                    }
                    check(nil)
                } label: {
                    Text("Download Laya and check now").font(.headline).frame(maxWidth: .infinity).padding(.vertical, 6)
                }
                .buttonStyle(.borderedProminent).tint(Color.sjSpark).foregroundStyle(.black)
                .disabled(listing == nil)
                Button { check(nil) } label: {
                    Text("Check with Apple Intelligence").frame(maxWidth: .infinity).padding(.vertical, 4)
                }
                .buttonStyle(.bordered).tint(Color.sjText)
            }
        }
        .buttonBorderShape(.capsule)
        .controlSize(.large)
        .padding(.horizontal, 24)
        .padding(.vertical, 12)
        .background(.bar)
    }
}
