import Foundation
@testable import LayaKit

/// Checkpoints the tests can use: .models/<name> in the package
/// (scripts/fetch-model.sh), or $LAYAKIT_MODELS/<name>. Tests that need one
/// are skipped when it is absent, so `swift test` needs no download.
enum Local {
    static let models: URL = {
        if let env = ProcessInfo.processInfo.environment["LAYAKIT_MODELS"] { return URL(fileURLWithPath: env) }
        return URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent(".models")
    }()

    static func checkpoint(_ name: String) -> URL { models.appendingPathComponent(name) }

    static func hasTokenizer(_ name: String) -> Bool {
        FileManager.default.fileExists(atPath: checkpoint(name).appendingPathComponent("tokenizer/tokenizer.json").path)
    }

    /// A whole checkpoint, verified by fetch-model.sh.
    static func hasModel(_ name: String) -> Bool {
        let dir = checkpoint(name)
        guard let data = try? Data(contentsOf: dir.appendingPathComponent("coreml_config.json")),
            let manifest = try? CheckpointManifest(data: data)
        else { return false }
        return manifest.files.allSatisfy { f in
            ((try? FileManager.default.attributesOfItem(atPath: dir.appendingPathComponent(f.path).path))?[.size] as? NSNumber)?.int64Value == f.bytes
        }
    }

    nonisolated(unsafe) static var tokenizers: [String: Tokenizer] = [:]
    static let lock = NSLock()

    static func tokenizer(_ name: String) throws -> Tokenizer {
        lock.lock()
        defer { lock.unlock() }
        if let t = tokenizers[name] { return t }
        let t = try Tokenizer(directory: checkpoint(name).appendingPathComponent("tokenizer"))
        tokenizers[name] = t
        return t
    }

    nonisolated(unsafe) static var models_: [String: LayaModel] = [:]

    static func model(_ name: String) throws -> LayaModel {
        lock.lock()
        defer { lock.unlock() }
        if let m = models_[name] { return m }
        let m = try LayaModel(directory: checkpoint(name))
        models_[name] = m
        return m
    }
}

enum Fixture {
    static func data(_ name: String) throws -> Data {
        guard let url = Bundle.module.url(forResource: "Fixtures/\(name)", withExtension: nil) else {
            throw CocoaError(.fileNoSuchFile)
        }
        return try Data(contentsOf: url)
    }

    static func json(_ name: String) throws -> JSONValue { try JSONValue(parsing: data(name)) }
}
