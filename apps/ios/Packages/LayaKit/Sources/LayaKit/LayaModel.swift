import CoreML
import Foundation

/// What a checkpoint's coreml_config.json says about itself.
public struct CheckpointManifest: Sendable {
    public enum Format: String, Sendable {
        /// One Core ML graph over token ids (CPU+GPU, enumerated lengths).
        case standard = "laya-coreml"
        /// The Neural Engine rewrite: the host looks up embeddings and builds
        /// masks; the graph takes them at one fixed length.
        case ane = "laya-coreml-ane"
    }

    public struct File: Sendable {
        public let path: String
        public let bytes: Int64
        public let sha256: String
    }

    public let format: Format
    public let repository: String?
    public let batchSize: Int
    public let maxLength: Int
    public let maxOptions: Int
    /// Sequence lengths the graph accepts, ascending.
    public let lengths: [Int]
    public let files: [File]
    public let packageSHA256: String?

    public init(data: Data) throws {
        guard let root = try JSONSerialization.jsonObject(with: data) as? [String: Any],
            let format = (root["format"] as? String).flatMap(Format.init(rawValue:)), (root["format_version"] as? Int) == 1,
            let shape = root["shape"] as? [String: Any], let maxLength = shape["max_length"] as? Int
        else { throw LayaError.unsupported("coreml_config.json is not a laya-coreml format 1 manifest") }
        self.format = format
        repository = root["repository"] as? String
        batchSize = shape["batch_size"] as? Int ?? 1
        self.maxLength = maxLength
        maxOptions = shape["max_options"] as? Int ?? 32
        if let lengths = shape["lengths"] as? [Int], !lengths.isEmpty {
            self.lengths = lengths.sorted()
        } else if shape["flexible"] as? Bool == true {
            // RangeDim exports: every multiple of 16 in range. laya-coreml
            // refuses these on the GPU (they failed its fidelity checks);
            // they are only run on the CPU here.
            let lo = max(16, shape["min_length"] as? Int ?? 16)
            self.lengths = Array(stride(from: lo, through: maxLength, by: 16))
        } else {
            self.lengths = [maxLength]
        }
        files = ((root["files"] as? [String: [String: Any]]) ?? [:]).compactMap { path, info in
            guard let sha = info["sha256"] as? String else { return nil }
            return File(path: path, bytes: (info["bytes"] as? NSNumber)?.int64Value ?? 0, sha256: sha)
        }.sorted { $0.path < $1.path }
        packageSHA256 = root["package_sha256"] as? String
        flexibleRange = shape["flexible"] as? Bool == true && (shape["lengths"] as? [Int] ?? []).isEmpty
    }

    let flexibleRange: Bool

    /// The compute units the checkpoint was validated on.
    public var defaultComputeUnits: MLComputeUnits {
        switch format {
        case .ane: .cpuAndNeuralEngine
        case .standard: flexibleRange ? .cpuOnly : .cpuAndGPU
        }
    }
}

/// A downloaded Laya checkpoint, loaded: tokenizer, calibration and the
/// compiled Core ML model(s). Compiled models are kept beside the checkpoint,
/// so only the first load of a checkpoint (or of a sequence length) pays the
/// compiler.
///
/// A standard checkpoint runs as fixed-length copies of its graph, one per
/// length bucket, made and loaded when a question first needs that length
/// (`FixedShape` says why). At most `keepLoaded` stay in memory — each holds
/// the weights — and a question goes to the smallest loaded bucket it fits
/// before another is loaded: padding costs milliseconds, a load seconds.
///
/// `logits` is safe from any thread; calls run one at a time (the GPU and
/// the Neural Engine take them one at a time anyway).
public final class LayaModel: @unchecked Sendable {
    public let directory: URL
    public let manifest: CheckpointManifest
    public let tokenizer: Tokenizer
    public let calibration: LayaCalibration
    /// The token budget of one question: the checkpoint's context, or the
    /// graph's fixed length when that is shorter (the ANE bundles: 96).
    public let maxLength: Int
    public let headMaxLength: Int
    public let computeUnits: MLComputeUnits
    /// The lengths a question is padded to (fixed-length mode), ascending.
    public let buckets: [Int]
    public let keepLoaded: Int
    let configuration: MLModelConfiguration
    /// One graph for every length: an ANE bundle (fixed already), or the
    /// enumerated graph when fixing it failed.
    var single: MLModel?
    /// Fixed-length graphs in memory, least recently used first.
    var loaded: [(length: Int, model: MLModel)] = []
    let host: HostWeights?
    private let lock = NSLock()

    /// The lengths fixed mode uses: coarse, so a check touches few of them.
    public static let defaultBuckets = [128, 256, 512, 1024]

    /// - Parameters:
    ///   - directory: a checkpoint as laid out on the Hub (coreml_config.json,
    ///     rl_agent_config.json, tokenizer/, model.mlpackage, …).
    ///   - computeUnits: nil = what the checkpoint was validated on.
    ///   - keepLoaded: fixed-length graphs kept in memory at once; nil = 2 on
    ///     a Mac, 1 on a phone.
    public init(directory: URL, computeUnits: MLComputeUnits? = nil, keepLoaded: Int? = nil) throws {
        self.directory = directory
        manifest = try CheckpointManifest(data: Data(contentsOf: directory.appendingPathComponent("coreml_config.json")))
        let agent = try JSONSerialization.jsonObject(with: Data(contentsOf: directory.appendingPathComponent("rl_agent_config.json"))) as? [String: Any] ?? [:]
        calibration = try LayaCalibration(agentConfig: agent)
        tokenizer = try Tokenizer(directory: directory.appendingPathComponent("tokenizer"))
        maxLength = min(agent["max_len"] as? Int ?? 512, manifest.maxLength)
        headMaxLength = agent["head_max_len"] as? Int ?? 192
        guard manifest.batchSize >= 1, manifest.maxOptions >= 2 else { throw LayaError.unsupported("shape") }
        #if os(macOS)
        self.keepLoaded = max(1, keepLoaded ?? 2)
        #else
        self.keepLoaded = max(1, keepLoaded ?? 1)
        #endif
        self.computeUnits = computeUnits ?? manifest.defaultComputeUnits
        configuration = MLModelConfiguration()
        configuration.computeUnits = self.computeUnits
        let manifest = self.manifest
        if manifest.format == .standard, manifest.lengths.count > 1, Self.fixedShapesWork(self.computeUnits) {
            var b = Self.defaultBuckets.filter { manifest.lengths.contains($0) && $0 <= manifest.maxLength }
            if b.last != manifest.maxLength { b.append(manifest.maxLength) }
            buckets = b
            host = nil
        } else {
            buckets = [manifest.lengths.last ?? manifest.maxLength]
            host = manifest.format == .ane ? try HostWeights(directory: directory, length: manifest.maxLength) : nil
            single = try MLModel(contentsOf: try Self.compiled(directory: directory, manifest: manifest), configuration: configuration)
        }
    }

    /// Fixed-length graphs plan onto the GPU. Pinned to other compute units
    /// E5RT fails to bind them at prediction ("No memory object bound to
    /// port", an Objective-C exception that ends the process), and the
    /// simulator has no GPU for Core ML: those keep the enumerated graph.
    static func fixedShapesWork(_ units: MLComputeUnits) -> Bool {
        #if targetEnvironment(simulator)
        return false
        #else
        return units == .cpuAndGPU
        #endif
    }

    var stamp: String { "\(manifest.packageSHA256 ?? "?") \(ProcessInfo.processInfo.operatingSystemVersionString)" }

    /// The package as it ships, compiled: model.mlmodelc beside it, compiled
    /// on first use and again when the package or the OS changes.
    static func compiled(directory: URL, manifest: CheckpointManifest) throws -> URL {
        let fm = FileManager.default
        let target = directory.appendingPathComponent("model.mlmodelc")
        let stamp = directory.appendingPathComponent("model.mlmodelc.stamp")
        let want = "\(manifest.packageSHA256 ?? "?") \(ProcessInfo.processInfo.operatingSystemVersionString)"
        if fm.fileExists(atPath: target.path), (try? String(contentsOf: stamp, encoding: .utf8)) == want {
            return target
        }
        let package = directory.appendingPathComponent("model.mlpackage")
        let temp = try MLModel.compileModel(at: package)
        defer { try? fm.removeItem(at: temp) }
        // One copy of the weights on disk: the compiler's is the package's.
        let weights = package.appendingPathComponent("Data/com.apple.CoreML/weights/weight.bin")
        let compiledWeights = temp.appendingPathComponent("weights/weight.bin")
        if fm.fileExists(atPath: compiledWeights.path), FixedShape.size(compiledWeights) == FixedShape.size(weights) {
            try fm.removeItem(at: compiledWeights)
            try FixedShape.link(weights, compiledWeights)
        }
        try? fm.removeItem(at: target)
        try fm.moveItem(at: temp, to: target)
        try want.write(to: stamp, atomically: true, encoding: .utf8)
        return target
    }

    /// Makes (and loads) the graphs for these lengths ahead of the first
    /// question — after a download, say — so a check does not wait on the
    /// compiler. nil = the smallest bucket.
    public func prepare(lengths: [Int]? = nil) throws {
        lock.lock()
        defer { lock.unlock() }
        guard single == nil else { return }
        for n in lengths ?? [buckets[0]] {
            if let b = buckets.first(where: { $0 >= n }) { _ = try graph(fixed: b) }
        }
    }

    /// Lays one question over the state, within this model's budget.
    public func sequence(state: LayaState, question: LayaQuestion) throws -> LayaSequence {
        let seq = try tokenizer.sequence(state: state, question: question, maxLen: maxLength, headMaxLen: headMaxLength)
        guard seq.markers.count <= manifest.maxOptions else {
            throw LayaError.capacity("\(seq.markers.count) options; this checkpoint takes at most \(manifest.maxOptions)")
        }
        return seq
    }

    /// The graph and padded length for n tokens. Called under lock.
    func graph(for n: Int) throws -> (MLModel, Int) {
        if let single {
            guard let length = manifest.lengths.first(where: { $0 >= n }) else {
                throw LayaError.capacity("\(n) tokens; this checkpoint takes at most \(manifest.maxLength)")
            }
            return (single, length)
        }
        guard let ideal = buckets.firstIndex(where: { $0 >= n }) else {
            throw LayaError.capacity("\(n) tokens; this checkpoint takes at most \(manifest.maxLength)")
        }
        // A loaded graph one bucket up (about twice the compute) beats
        // seconds of loading; further up, the load pays for itself in a check.
        let ceiling = buckets[min(ideal + 1, buckets.count - 1)]
        if let hit = loaded.filter({ $0.length >= n && $0.length <= ceiling }).min(by: { $0.length < $1.length }) {
            return (try graph(fixed: hit.length), hit.length)
        }
        return (try graph(fixed: buckets[ideal]), buckets[ideal])
    }

    /// The fixed-length graph, loaded (evicting the least recently used when
    /// full) and marked most recently used. Called under lock.
    func graph(fixed length: Int) throws -> MLModel {
        if let i = loaded.firstIndex(where: { $0.length == length }) {
            let hit = loaded.remove(at: i)
            loaded.append(hit)
            return hit.model
        }
        let url = try FixedShape.compiled(checkpoint: directory, length: length, stamp: stamp)
        if loaded.count >= keepLoaded { loaded.removeFirst(loaded.count - keepLoaded + 1) }
        let model = try MLModel(contentsOf: url, configuration: configuration)
        loaded.append((length, model))
        return model
    }

    /// The option logits of one sequence (first `markers.count` are real).
    public func logits(_ seq: LayaSequence) throws -> [Float] {
        lock.lock()
        defer { lock.unlock() }
        let (model, length) = try graph(for: seq.ids.count)
        let input: MLFeatureProvider =
            if let host { try host.inputs(seq, length: length, pad: tokenizer.padID) } else {
                try Self.tokenInputs(seq, length: length, batch: manifest.batchSize, options: manifest.maxOptions, pad: tokenizer.padID)
            }
        let out = try model.prediction(from: input)
        let logits: MLMultiArray?
        if host == nil {
            logits = out.featureValue(for: "logits")?.multiArrayValue
        } else {
            // The ANE graph's output names are traced identifiers: its logits
            // are the output with one value per option slot.
            logits = out.featureNames.lazy.compactMap { out.featureValue(for: $0)?.multiArrayValue }.first { $0.count == self.manifest.maxOptions }
        }
        guard let logits else { throw LayaError.model("the model returned no logits") }
        let k = manifest.maxOptions
        var row = [Float](repeating: 0, count: k)
        for i in 0..<k { row[i] = logits[i].floatValue }
        guard row.prefix(seq.markers.count).allSatisfy(\.isFinite) else { throw LayaError.model("non-finite Core ML outputs") }
        return row
    }

    /// input_ids/attention_mask [B, L], marker_pos/marker_mask [B, K], qtype
    /// [B]: row 0 is the question; spare rows of a fixed batch hold one
    /// valid key each (laya's collate_items).
    static func tokenInputs(_ seq: LayaSequence, length: Int, batch: Int, options k: Int, pad: Int) throws -> MLFeatureProvider {
        func array(_ shape: [Int]) throws -> MLMultiArray {
            try MLMultiArray(shape: shape.map { NSNumber(value: $0) }, dataType: .int32)
        }
        let ids = try array([batch, length]), mask = try array([batch, length])
        let pos = try array([batch, k]), mmask = try array([batch, k]), qtype = try array([batch])
        let pIds = ids.dataPointer.bindMemory(to: Int32.self, capacity: batch * length)
        let pMask = mask.dataPointer.bindMemory(to: Int32.self, capacity: batch * length)
        let pPos = pos.dataPointer.bindMemory(to: Int32.self, capacity: batch * k)
        let pMM = mmask.dataPointer.bindMemory(to: Int32.self, capacity: batch * k)
        let pQ = qtype.dataPointer.bindMemory(to: Int32.self, capacity: batch)
        for i in 0..<(batch * length) { pIds[i] = Int32(pad); pMask[i] = 0 }
        for i in 0..<(batch * k) { pPos[i] = 0; pMM[i] = 0 }
        for b in 0..<batch { pMask[b * length] = 1; pQ[b] = 0 }
        for (t, id) in seq.ids.enumerated() { pIds[t] = id; pMask[t] = 1 }
        for (j, m) in seq.markers.enumerated() { pPos[j] = m; pMM[j] = 1 }
        pQ[0] = seq.kind.index
        return try MLDictionaryFeatureProvider(dictionary: [
            "input_ids": ids, "attention_mask": mask, "marker_pos": pos, "marker_mask": mmask, "qtype": qtype,
        ])
    }
}
