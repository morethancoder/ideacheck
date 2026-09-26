import Foundation
import FoundationModels
import LayaKit
import Observation

/// What this iPhone can judge with: Apple Intelligence when it is on, the
/// Laya checkpoints downloaded here, and the ones the Hugging Face Hub lists
/// now (`LayaDownloader.available()` — read live, never a list in the app).
/// Downloads run here, not in a view, so they go on when Settings closes.
@MainActor
@Observable
final class OnDeviceJudges {
    /// A checkpoint as the Hub lists it, with what its manifest says.
    struct Listing: Identifiable, Hashable, Sendable {
        var id: String
        /// Bytes to download; nil when its manifest could not be read.
        var bytes: Int64?
        var maxLength: Int?
        var maxOptions: Int?
        /// Why it cannot run a check; nil when it can (or is not known yet).
        var problem: String?

        /// The Hub name without its "-coreml" (every one listed is Core ML).
        var name: String {
            let last = id.split(separator: "/").last.map(String.init) ?? id
            return last.hasSuffix("-coreml") ? String(last.dropLast(7)) : last
        }
        var isTypedDecisions: Bool { id.contains("typed-decisions") }
        var canCheck: Bool { problem == nil && bytes != nil }
    }

    enum Download: Equatable, Sendable {
        case running(done: Int64, total: Int64)
        /// Loading (and on a phone compiling) the model after the download.
        case preparing
        case failed(String)
    }

    enum HubState: Equatable, Sendable {
        case idle, loading, loaded
        case failed(String)
    }

    /// Why Apple's model cannot judge now; nil when it can.
    private(set) var appleProblem: String?
    private(set) var hub: [Listing] = []
    private(set) var hubState: HubState = .idle
    /// Complete checkpoints on this device (and, in Debug, `-sjLayaDir`).
    private(set) var local: [LocalCheckpoint] = []
    /// Bytes already on disk, by id: complete and partial downloads alike.
    private(set) var onDisk: [String: Int64] = [:]
    private(set) var downloads: [String: Download] = [:]
    private(set) var preference: JudgePreference

    let downloader: LayaDownloader?
    /// Debug: a checkpoint directory on the host (`-sjLayaDir <path>`), read
    /// in place of a download in the simulator.
    let debugDirectory: URL?
    private let defaults: UserDefaults
    private var tasks: [String: Task<Void, Never>] = [:]

    static let preferenceKey = SettingsKey.onDeviceJudge

    init(downloader: LayaDownloader? = try? LayaDownloader(), debugDirectory: URL? = OnDeviceJudges.launchDirectory,
         defaults: UserDefaults = .standard) {
        self.downloader = downloader
        self.debugDirectory = debugDirectory
        self.defaults = defaults
        preference = JudgePreference(rawValue: defaults.string(forKey: Self.preferenceKey))
        refreshDevice()
    }

    static var launchDirectory: URL? {
        #if DEBUG
        guard let path = UserDefaults.standard.string(forKey: "sjLayaDir"), !path.isEmpty else { return nil }
        return URL(fileURLWithPath: (path as NSString).expandingTildeInPath, isDirectory: true)
        #else
        return nil
        #endif
    }

    // MARK: What checks use

    /// The judge a check on this iPhone uses now (JudgeResolver).
    var judge: OnDeviceJudge? {
        JudgeResolver.resolve(preference, appleAvailable: appleProblem == nil, downloaded: local)
    }

    /// Whether Apple's model writes (the extract and summary steps).
    var writerAvailable: Bool { appleProblem == nil }

    var checker: OnDeviceChecker { OnDeviceChecker(judge: judge, writer: writerAvailable) }

    /// The checkpoint to offer: typed decisions if the Hub lists it, else the
    /// first one that can run a check.
    var recommended: Listing? {
        hub.first { $0.isTypedDecisions && $0.canCheck } ?? hub.first(where: \.canCheck)
    }

    func isDownloaded(_ id: String) -> Bool { local.contains { $0.id == id } }

    func choose(_ preference: JudgePreference) {
        self.preference = preference
        defaults.set(preference.rawValue, forKey: Self.preferenceKey)
    }

    // MARK: Refreshing

    /// Re-reads Apple Intelligence's availability and the checkpoints on disk.
    func refreshDevice() {
        appleProblem = SystemLanguageModel.default.availability.problem
        var complete: [LocalCheckpoint] = []
        var disk: [String: Int64] = [:]
        if let downloader {
            for c in downloader.local() {
                disk[c.id] = c.bytes
                if c.complete {
                    let dir = downloader.directory(for: c.id)
                    complete.append(LocalCheckpoint(id: c.id, directory: dir, problem: Self.problem(at: dir)))
                }
            }
        }
        if let dir = debugDirectory, let manifest = Self.manifest(at: dir) {
            let id = manifest.repository ?? dir.lastPathComponent
            complete.removeAll { $0.id == id }
            complete.insert(LocalCheckpoint(id: id, directory: dir, problem: LayaFit.problem(manifest)), at: 0)
        }
        local = complete
        onDisk = disk
    }

    static func manifest(at dir: URL) -> CheckpointManifest? {
        (try? Data(contentsOf: dir.appendingPathComponent("coreml_config.json"))).flatMap { try? CheckpointManifest(data: $0) }
    }

    static func problem(at dir: URL) -> String? {
        guard let manifest = manifest(at: dir) else { return "Its manifest can't be read." }
        return LayaFit.problem(manifest)
    }

    /// Lists the Hub's checkpoints now, each with its manifest's size and capacity.
    func refreshHub() async {
        guard let downloader else {
            hubState = .failed("This iPhone has no room set aside for models.")
            return
        }
        if hubState == .loading { return }
        hubState = .loading
        do {
            let listed = try await downloader.available()
            let details = await withTaskGroup(of: (String, LayaDownloader.Details?).self) { group in
                for l in listed {
                    group.addTask { (l.id, try? await downloader.details(l.id)) }
                }
                var out: [String: LayaDownloader.Details] = [:]
                for await (id, d) in group { if let d { out[id] = d } }
                return out
            }
            hub = listed.map { l in
                guard let d = details[l.id] else { return Listing(id: l.id) }
                return Listing(id: l.id, bytes: d.bytes, maxLength: d.manifest.maxLength, maxOptions: d.manifest.maxOptions,
                               problem: LayaFit.problem(d.manifest))
            }
            hubState = .loaded
        } catch {
            hubState = .failed("Hugging Face didn't answer (\(error.localizedDescription)).")
        }
    }

    // MARK: Downloading

    /// Starts (or resumes) a download; when it ends the model is loaded, so
    /// the first check does not wait on it.
    func download(_ id: String) {
        guard tasks[id] == nil, let downloader else { return }
        let total = hub.first { $0.id == id }?.bytes ?? 0
        let have = onDisk[id] ?? 0
        if let free = Self.freeSpace(), total > 0, free < (total - have) + total / 5 {
            downloads[id] = .failed("It needs \(Self.size(total - have)) free; this iPhone has \(Self.size(free)).")
            return
        }
        downloads[id] = .running(done: min(have, total), total: total)
        // The store lives as long as the app: the task holds it.
        tasks[id] = Task {
            do {
                let dir = try await downloader.download(id) { p in
                    Task { @MainActor [weak self] in self?.progress(id, p) }
                }
                downloads[id] = .preparing
                refreshDevice()
                try await LayaDeviceJudge.preload(dir)
                downloads[id] = nil
            } catch {
                let stopped = Task.isCancelled || error is CancellationError || (error as? URLError)?.code == .cancelled
                downloads[id] = stopped ? nil : .failed(Self.describe(error))
            }
            tasks[id] = nil
            refreshDevice()
        }
    }

    private func progress(_ id: String, _ p: LayaDownloader.Progress) {
        guard case .running(let done, _) = downloads[id], p.done >= done else { return }
        downloads[id] = .running(done: p.done, total: p.total)
    }

    /// Stops a download where it is; `download` resumes it.
    func pause(_ id: String) {
        tasks[id]?.cancel()
        tasks[id] = nil
        downloads[id] = nil
        refreshDevice()
    }

    func remove(_ id: String) {
        pause(id)
        LayaDeviceJudge.unload()
        try? downloader?.remove(id)
        if preference == .laya(id) { choose(.automatic) }
        refreshDevice()
    }

    static func describe(_ error: any Error) -> String {
        if let url = error as? URLError {
            switch url.code {
            case .notConnectedToInternet, .networkConnectionLost: return "The connection dropped. Resume when you're back online."
            case .timedOut: return "Hugging Face stopped answering. Try again."
            default: break
            }
        }
        return error.localizedDescription
    }

    static func freeSpace() -> Int64? {
        let values = try? URL.applicationSupportDirectory.resourceValues(forKeys: [.volumeAvailableCapacityForImportantUsageKey])
        return values?.volumeAvailableCapacityForImportantUsage
    }

    static func size(_ bytes: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: bytes, countStyle: .file)
    }
}
