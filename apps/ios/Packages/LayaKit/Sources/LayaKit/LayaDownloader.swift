import CryptoKit
import Foundation

/// Finds Laya Core ML checkpoints on the Hugging Face Hub and keeps them on
/// this device.
///
/// The list is read live from the Hub every time (`available()`): a model
/// picker must follow what is published, never a list written into the app.
/// A download fetches the files the checkpoint's own coreml_config.json names,
/// resumes a partial file where it stopped, and checks each against the
/// SHA-256 in that manifest before the checkpoint counts as present.
public final class LayaDownloader: Sendable {
    public static let author = "aac6fef"

    /// A checkpoint as the Hub lists it.
    public struct Listing: Sendable, Hashable, Identifiable {
        public let id: String  // "aac6fef/laya-typed-decisions-coreml"
        public let lastModified: String?
        public let downloads: Int?
        public let likes: Int?
        public var name: String { id.split(separator: "/").last.map(String.init) ?? id }
    }

    /// What a checkpoint is, from its manifest, before downloading it.
    public struct Details: Sendable {
        public let id: String
        public let manifest: CheckpointManifest
        /// Bytes to download.
        public let bytes: Int64
        public let files: [(path: String, bytes: Int64, sha256: String?)]
    }

    public let root: URL
    let session: URLSession
    let hub: URL

    /// - Parameter root: where checkpoints live, one directory each; nil =
    ///   Application Support/LayaKit/models, kept out of backups (the Hub can
    ///   give them back).
    public init(root: URL? = nil, session: URLSession = .shared, hub: URL = URL(string: "https://huggingface.co")!) throws {
        if let root {
            self.root = root
        } else {
            let support = try FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true)
            self.root = support.appendingPathComponent("LayaKit/models", isDirectory: true)
        }
        try FileManager.default.createDirectory(at: self.root, withIntermediateDirectories: true)
        var url = self.root
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try? url.setResourceValues(values)
        self.session = session
        self.hub = hub
    }

    // MARK: Listing

    /// Every Core ML checkpoint the author publishes, as the Hub lists them
    /// now: ids containing "-coreml" among `?author=aac6fef&search=laya`.
    public func available() async throws -> [Listing] {
        var c = URLComponents(url: hub.appendingPathComponent("api/models"), resolvingAgainstBaseURL: false)!
        c.queryItems = [URLQueryItem(name: "author", value: Self.author), URLQueryItem(name: "search", value: "laya")]
        let data = try await get(c.url!)
        guard let list = try JSONSerialization.jsonObject(with: data) as? [[String: Any]] else {
            throw LayaError.download("the Hub's model list is not a list")
        }
        return list.compactMap { m in
            guard let id = m["id"] as? String, id.contains("-coreml") else { return nil }
            return Listing(id: id, lastModified: m["lastModified"] as? String, downloads: m["downloads"] as? Int, likes: m["likes"] as? Int)
        }
        .sorted { $0.id < $1.id }
    }

    /// The checkpoint's manifest (format, token capacity, options) and the
    /// bytes it takes, from the Hub.
    public func details(_ id: String) async throws -> Details {
        let manifest = try CheckpointManifest(data: try await get(resolve(id, "coreml_config.json")))
        let tree = try await self.tree(id)
        let want = Self.wanted(tree.map(\.path), manifest: manifest)
        let sums = Dictionary(uniqueKeysWithValues: manifest.files.map { ($0.path, $0.sha256) })
        let files = tree.filter { want.contains($0.path) }.map { (path: $0.path, bytes: $0.bytes, sha256: sums[$0.path]) }
        return Details(id: id, manifest: manifest, bytes: files.reduce(0) { $0 + $1.bytes }, files: files)
    }

    /// The files a checkpoint needs: every file its manifest checks, the
    /// manifest itself, and its license, notice and validation record.
    static func wanted(_ paths: [String], manifest: CheckpointManifest) -> Set<String> {
        var want = Set(manifest.files.map(\.path))
        want.insert("coreml_config.json")
        for extra in ["LICENSE", "NOTICE", "validation.json"] where paths.contains(extra) { want.insert(extra) }
        return want
    }

    struct TreeEntry {
        let path: String
        let bytes: Int64
    }

    func tree(_ id: String) async throws -> [TreeEntry] {
        var c = URLComponents(url: hub.appendingPathComponent("api/models/\(id)/tree/main"), resolvingAgainstBaseURL: false)!
        c.queryItems = [URLQueryItem(name: "recursive", value: "1")]
        guard let list = try JSONSerialization.jsonObject(with: try await get(c.url!)) as? [[String: Any]] else {
            throw LayaError.download("the Hub's file list for \(id) is not a list")
        }
        return list.compactMap { f in
            guard f["type"] as? String == "file", let path = f["path"] as? String else { return nil }
            return TreeEntry(path: path, bytes: (f["size"] as? NSNumber)?.int64Value ?? 0)
        }
    }

    func resolve(_ id: String, _ path: String) -> URL {
        hub.appendingPathComponent("\(id)/resolve/main/\(path)")
    }

    func get(_ url: URL) async throws -> Data {
        let (data, response) = try await session.data(from: url)
        if let http = response as? HTTPURLResponse, http.statusCode != 200 {
            throw LayaError.download("\(url.path) answered \(http.statusCode)")
        }
        return data
    }

    // MARK: On this device

    /// Where a checkpoint lives here (whether or not it is downloaded).
    public func directory(for id: String) -> URL {
        root.appendingPathComponent(id.replacingOccurrences(of: "/", with: "--"), isDirectory: true)
    }

    static let completeMarker = ".complete"

    /// Whether the checkpoint is fully downloaded and verified.
    public func isDownloaded(_ id: String) -> Bool {
        FileManager.default.fileExists(atPath: directory(for: id).appendingPathComponent(Self.completeMarker).path)
    }

    /// The checkpoints on this device, complete or partial, by Hub id.
    public func local() -> [(id: String, complete: Bool, bytes: Int64)] {
        let dirs = (try? FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: nil)) ?? []
        return dirs.compactMap { dir in
            let name = dir.lastPathComponent
            guard name.contains("--") else { return nil }
            let id = name.replacingOccurrences(of: "--", with: "/")
            return (id, isDownloaded(id), Self.size(of: dir))
        }
        .sorted { $0.id < $1.id }
    }

    /// Bytes on disk under a checkpoint, the compiled model included.
    public func diskSize(_ id: String) -> Int64 { Self.size(of: directory(for: id)) }

    /// Each file once: the compiled models hard-link the checkpoint's weights.
    static func size(of dir: URL) -> Int64 {
        let keys: [URLResourceKey] = [.totalFileAllocatedSizeKey, .isRegularFileKey, .fileResourceIdentifierKey]
        guard let walk = FileManager.default.enumerator(at: dir, includingPropertiesForKeys: keys) else { return 0 }
        var total: Int64 = 0
        var seen = Set<String>()
        for case let url as URL in walk {
            guard let v = try? url.resourceValues(forKeys: Set(keys)), v.isRegularFile == true else { continue }
            if let id = v.fileResourceIdentifier.map({ "\($0)" }), !seen.insert(id).inserted { continue }
            total += Int64(v.totalFileAllocatedSize ?? 0)
        }
        return total
    }

    public func remove(_ id: String) throws {
        let dir = directory(for: id)
        if FileManager.default.fileExists(atPath: dir.path) { try FileManager.default.removeItem(at: dir) }
    }

    // MARK: Downloading

    public struct Progress: Sendable {
        public let file: String
        public let done: Int64
        public let total: Int64
        public var fraction: Double { total > 0 ? Double(done) / Double(total) : 0 }
    }

    /// Downloads the checkpoint into `directory(for:)` and returns that
    /// directory. Files already complete are kept (after their checksum);
    /// a partial one resumes; a file that fails its checksum is fetched again
    /// once, then the download fails. Cancelling the task stops it where it
    /// is, and the next call resumes.
    @discardableResult
    public func download(_ id: String, progress: @escaping @Sendable (Progress) -> Void = { _ in }) async throws -> URL {
        let details = try await self.details(id)
        let dir = directory(for: id)
        let fm = FileManager.default
        try fm.createDirectory(at: dir, withIntermediateDirectories: true)
        try? fm.removeItem(at: dir.appendingPathComponent(Self.completeMarker))
        let total = details.bytes
        var before: Int64 = 0
        for file in details.files {
            let target = dir.appendingPathComponent(file.path)
            try fm.createDirectory(at: target.deletingLastPathComponent(), withIntermediateDirectories: true)
            var attempts = 0
            while true {
                try Task.checkCancellation()
                attempts += 1
                if !Self.complete(target, bytes: file.bytes) {
                    let base = before
                    try await fetch(resolve(id, file.path), to: target, bytes: file.bytes) { got in
                        progress(Progress(file: file.path, done: base + got, total: total))
                    }
                }
                guard let sum = file.sha256 else { break }
                if try Self.sha256(of: target) == sum { break }
                try? fm.removeItem(at: target)
                if attempts >= 2 { throw LayaError.download("\(file.path) does not match its checksum") }
            }
            before += file.bytes
            progress(Progress(file: file.path, done: before, total: total))
        }
        try Data().write(to: dir.appendingPathComponent(Self.completeMarker))
        return dir
    }

    static func complete(_ url: URL, bytes: Int64) -> Bool {
        guard let size = (try? FileManager.default.attributesOfItem(atPath: url.path))?[.size] as? NSNumber else { return false }
        return size.int64Value == bytes
    }

    static func sha256(of url: URL) throws -> String {
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        var hash = SHA256()
        while let block = try handle.read(upToCount: 8 << 20), !block.isEmpty { hash.update(data: block) }
        return hash.finalize().map { String(format: "%02x", $0) }.joined()
    }

    /// Streams url into target, resuming from `target.part` when there is
    /// one (an HTTP Range request); `target` appears only when complete.
    func fetch(_ url: URL, to target: URL, bytes: Int64, progress: @escaping @Sendable (Int64) -> Void) async throws {
        let part = target.appendingPathExtension("part")
        let fm = FileManager.default
        if !fm.fileExists(atPath: part.path) { fm.createFile(atPath: part.path, contents: nil) }
        var have = ((try? fm.attributesOfItem(atPath: part.path))?[.size] as? NSNumber)?.int64Value ?? 0
        if have > bytes { try Data().write(to: part); have = 0 }
        if have < bytes || bytes == 0 {
            var request = URLRequest(url: url)
            if have > 0 { request.setValue("bytes=\(have)-", forHTTPHeaderField: "Range") }
            let handle = try FileHandle(forWritingTo: part)
            defer { try? handle.close() }
            // A server that ignores the range sends the whole file: the sink
            // then writes from the top.
            try await Sink(handle: handle, start: have, progress: progress).run(request, session: session)
        }
        try? fm.removeItem(at: target)
        try fm.moveItem(at: part, to: target)
    }
}

/// Writes a response body to a file as it arrives, reporting bytes.
final class Sink: NSObject, URLSessionDataDelegate, @unchecked Sendable {
    let handle: FileHandle
    var written: Int64
    let progress: @Sendable (Int64) -> Void
    var status = 0
    var failure: Error?
    var done: CheckedContinuation<Void, Error>?
    var lastReport = Date.distantPast

    init(handle: FileHandle, start: Int64, progress: @escaping @Sendable (Int64) -> Void) {
        self.handle = handle
        written = start
        self.progress = progress
    }

    func run(_ request: URLRequest, session: URLSession) async throws {
        let local = URLSession(configuration: session.configuration, delegate: self, delegateQueue: nil)
        defer { local.finishTasksAndInvalidate() }
        let task = local.dataTask(with: request)
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { c in
                done = c
                task.resume()
            }
        } onCancel: {
            task.cancel()
        }
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
                    completionHandler: @escaping (URLSession.ResponseDisposition) -> Void) {
        status = (response as? HTTPURLResponse)?.statusCode ?? 0
        if status == 200, written > 0 {
            // A full body where the rest was asked for: write it from the top.
            try? handle.truncate(atOffset: 0)
            written = 0
        } else {
            _ = try? handle.seekToEnd()
        }
        guard status == 200 || status == 206 else {
            failure = LayaError.download("\(dataTask.originalRequest?.url?.lastPathComponent ?? "file") answered \(status)")
            completionHandler(.cancel)
            return
        }
        completionHandler(.allow)
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        do {
            try handle.write(contentsOf: data)
            written += Int64(data.count)
            if Date().timeIntervalSince(lastReport) > 0.1 {
                lastReport = Date()
                progress(written)
            }
        } catch {
            failure = error
            dataTask.cancel()
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        let c = done
        done = nil
        if let failure { c?.resume(throwing: failure) } else if let error { c?.resume(throwing: error) } else { c?.resume() }
    }
}
