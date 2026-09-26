import CryptoKit
import Foundation
import Testing
@testable import LayaKit

/// The downloader against a fake Hub (a URLProtocol): no test touches the
/// network.
@Suite(.serialized)
struct DownloaderTests {
    static let repo = "aac6fef/laya-test-coreml"
    static let weights = Data((0..<300_000).map { UInt8(truncatingIfNeeded: $0 &* 31) })
    static let tokenizer = Data(#"{"model":{}}"#.utf8)

    static func sha(_ d: Data) -> String { SHA256.hash(data: d).map { String(format: "%02x", $0) }.joined() }

    static func manifest(weightSHA: String = sha(weights)) -> Data {
        let m: [String: Any] = [
            "format": "laya-coreml", "format_version": 1,
            "shape": ["batch_size": 1, "max_length": 1024, "max_options": 32, "flexible": true, "lengths": [16, 1024]],
            "files": [
                "model.mlpackage/Data/com.apple.CoreML/weights/weight.bin": ["bytes": weights.count, "sha256": weightSHA],
                "tokenizer/tokenizer.json": ["bytes": tokenizer.count, "sha256": sha(tokenizer)],
            ],
        ]
        return try! JSONSerialization.data(withJSONObject: m)
    }

    func hub(manifest: Data = manifest()) -> (LayaDownloader, URL) {
        FakeHub.reset()
        FakeHub.files = [
            "coreml_config.json": manifest,
            "model.mlpackage/Data/com.apple.CoreML/weights/weight.bin": Self.weights,
            "tokenizer/tokenizer.json": Self.tokenizer,
            "README.md": Data("readme".utf8),
            "LICENSE": Data("license".utf8),
        ]
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [FakeHub.self]
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("layakit-\(UUID().uuidString)")
        return (try! LayaDownloader(root: root, session: URLSession(configuration: config), hub: URL(string: "https://hub.test")!), root)
    }

    @Test func listsOnlyCoreMLCheckpointsLive() async throws {
        let (d, root) = hub()
        defer { try? FileManager.default.removeItem(at: root) }
        let list = try await d.available()
        #expect(list.map(\.id) == ["aac6fef/laya-coreml", "aac6fef/laya-multilingual-coreml-ane"])
        #expect(FakeHub.requests.contains { $0.contains("/api/models?author=aac6fef&search=laya") })
    }

    @Test func downloadsVerifiesAndResumes() async throws {
        let (d, root) = hub()
        defer { try? FileManager.default.removeItem(at: root) }
        let details = try await d.details(Self.repo)
        #expect(details.manifest.maxLength == 1024)
        // What the manifest checks, the manifest itself and the license; not the README.
        #expect(Set(details.files.map(\.path)) == ["coreml_config.json", "LICENSE", "model.mlpackage/Data/com.apple.CoreML/weights/weight.bin", "tokenizer/tokenizer.json"])

        // A download that stopped a third of the way through the weights.
        let dir = d.directory(for: Self.repo)
        let part = dir.appendingPathComponent("model.mlpackage/Data/com.apple.CoreML/weights/weight.bin.part")
        try FileManager.default.createDirectory(at: part.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Self.weights.prefix(100_000).write(to: part)

        let seen = Progressed()
        let got = try await d.download(Self.repo) { seen.add($0) }
        #expect(got == dir)
        #expect(d.isDownloaded(Self.repo))
        #expect(try Data(contentsOf: dir.appendingPathComponent("model.mlpackage/Data/com.apple.CoreML/weights/weight.bin")) == Self.weights)
        #expect(FakeHub.ranges.contains("bytes=100000-"))
        #expect(seen.last?.done == details.bytes && seen.last?.total == details.bytes)
        #expect(d.diskSize(Self.repo) >= details.bytes)
        #expect(d.local().map(\.id) == [Self.repo])
        try d.remove(Self.repo)
        #expect(!d.isDownloaded(Self.repo))
    }

    /// The real Hub, on demand: LAYAKIT_LIVE=1 swift test --filter live
    @Test(.enabled(if: ProcessInfo.processInfo.environment["LAYAKIT_LIVE"] != nil))
    func liveHub() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("layakit-live")
        let d = try LayaDownloader(root: root)
        let list = try await d.available()
        #expect(list.contains { $0.id == "aac6fef/laya-typed-decisions-coreml" })
        #expect(list.allSatisfy { $0.id.contains("-coreml") })
        for item in list {
            let details = try await d.details(item.id)
            print("hub \(item.id): \(details.manifest.format.rawValue), \(details.manifest.maxLength) tokens, \(details.manifest.maxOptions) options, \(ByteCountFormatter.string(fromByteCount: details.bytes, countStyle: .file))")
        }
    }

    @Test func refusesAFileThatFailsItsChecksum() async throws {
        let (d, root) = hub(manifest: Self.manifest(weightSHA: String(repeating: "0", count: 64)))
        defer { try? FileManager.default.removeItem(at: root) }
        await #expect(throws: LayaError.self) { try await d.download(Self.repo) }
        #expect(!d.isDownloaded(Self.repo))
    }
}

final class Progressed: @unchecked Sendable {
    private let lock = NSLock()
    private var items: [LayaDownloader.Progress] = []
    func add(_ p: LayaDownloader.Progress) { lock.lock(); items.append(p); lock.unlock() }
    var last: LayaDownloader.Progress? { lock.lock(); defer { lock.unlock() }; return items.last }
}

/// A Hugging Face Hub in a URLProtocol: the model list, one repo's tree, and
/// its files by resolve URL, honoring Range.
final class FakeHub: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var files: [String: Data] = [:]
    nonisolated(unsafe) static var requests: [String] = []
    nonisolated(unsafe) static var ranges: [String] = []
    static let lock = NSLock()

    static func reset() {
        lock.lock()
        files = [:]
        requests = []
        ranges = []
        lock.unlock()
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func stopLoading() {}

    override func startLoading() {
        guard let url = request.url else { return }
        Self.lock.lock()
        Self.requests.append(url.absoluteString)
        if let r = request.value(forHTTPHeaderField: "Range") { Self.ranges.append(r) }
        let files = Self.files
        Self.lock.unlock()
        let path = url.path
        var status = 200
        var body = Data()
        var headers: [String: String] = [:]
        if path == "/api/models" {
            let list: [[String: Any]] = [
                ["id": "aac6fef/laya-mlx"], ["id": "aac6fef/laya-multilingual-coreml-ane", "downloads": 3],
                ["id": "aac6fef/laya-coreml"], ["id": "aac6fef/laya-typed-decisions-mlx"],
            ]
            body = try! JSONSerialization.data(withJSONObject: list)
        } else if path == "/api/models/\(DownloaderTests.repo)/tree/main" {
            let tree = files.map { ["type": "file", "path": $0.key, "size": $0.value.count] as [String: Any] }
                + [["type": "directory", "path": "tokenizer", "size": 0]]
            body = try! JSONSerialization.data(withJSONObject: tree)
        } else if path.hasPrefix("/\(DownloaderTests.repo)/resolve/main/"), let file = files[String(path.dropFirst("/\(DownloaderTests.repo)/resolve/main/".count))] {
            body = file
            if let r = request.value(forHTTPHeaderField: "Range"), r.hasPrefix("bytes="), let from = Int(r.dropFirst(6).dropLast()) {
                status = 206
                body = Data(file.suffix(from: from))  // re-based: a slice keeps its indices
                headers["Content-Range"] = "bytes \(from)-\(file.count - 1)/\(file.count)"
            }
        } else {
            status = 404
        }
        let response = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: headers)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        // In chunks, as a network delivers it.
        var at = 0
        while at < body.count {
            let end = min(at + 65_536, body.count)
            client?.urlProtocol(self, didLoad: body.subdata(in: at..<end))
            at = end
        }
        client?.urlProtocolDidFinishLoading(self)
    }
}
