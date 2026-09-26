import CoreML
import Foundation

/// The part of an ANE checkpoint that runs on the CPU: the token and
/// question-type embeddings (host_weights.safetensors, memory-mapped) and the
/// attention masks the graph takes as inputs (laya-coreml's ANEAgent).
///
/// The action head, also in that file, is not run: it only feeds
/// `act_probability`, which a typed answer does not use.
final class HostWeights: @unchecked Sendable {
    let file: Data
    let tokens: Tensor
    let types: Tensor
    let width: Int
    let length: Int
    let window: Int

    struct Tensor {
        let dtype: String
        let rows: Int
        let cols: Int
        let offset: Int  // into file

        func row(_ r: Int, in file: Data, into out: UnsafeMutablePointer<Float16>, stride: Int) {
            file.withUnsafeBytes { raw in
                let base = raw.baseAddress!.advanced(by: offset)
                switch dtype {
                case "F16":
                    let p = base.assumingMemoryBound(to: Float16.self).advanced(by: r * cols)
                    for c in 0..<cols { out[c * stride] = p[c] }
                case "F32":
                    let p = base.assumingMemoryBound(to: Float.self).advanced(by: r * cols)
                    for c in 0..<cols { out[c * stride] = Float16(p[c]) }
                default:  // BF16
                    let p = base.assumingMemoryBound(to: UInt16.self).advanced(by: r * cols)
                    for c in 0..<cols { out[c * stride] = Float16(Float(bitPattern: UInt32(p[c]) << 16)) }
                }
            }
        }
    }

    init(directory: URL, length: Int) throws {
        file = try Data(contentsOf: directory.appendingPathComponent("host_weights.safetensors"), options: .alwaysMapped)
        guard file.count > 8 else { throw LayaError.unsupported("host_weights.safetensors is empty") }
        let headerLength = Int(file.withUnsafeBytes { $0.loadUnaligned(as: UInt64.self) }.littleEndian)
        guard 8 + headerLength <= file.count,
            let header = try JSONSerialization.jsonObject(with: file.subdata(in: 8..<8 + headerLength)) as? [String: Any]
        else { throw LayaError.unsupported("host_weights.safetensors has no header") }
        func tensor(_ name: String) throws -> Tensor {
            guard let t = header[name] as? [String: Any], let dtype = t["dtype"] as? String, ["F16", "F32", "BF16"].contains(dtype),
                let shape = t["shape"] as? [Int], shape.count == 2, let offsets = t["data_offsets"] as? [Int]
            else { throw LayaError.unsupported("host_weights.safetensors lacks \(name)") }
            return Tensor(dtype: dtype, rows: shape[0], cols: shape[1], offset: 8 + headerLength + offsets[0])
        }
        tokens = try tensor("encoder.embeddings.tok_embeddings.weight")
        types = try tensor("type_emb.weight")
        width = tokens.cols
        guard types.cols == width else { throw LayaError.unsupported("embedding widths differ") }
        self.length = length
        let encoder = try? JSONSerialization.jsonObject(with: Data(contentsOf: directory.appendingPathComponent("encoder/config.json"))) as? [String: Any]
        window = (encoder?["local_attention"] as? Int ?? 128) / 2
    }

    /// embeddings [1, W, 1, L], full_mask and local_mask [1, key, 1, query]
    /// (0 where attention may look, −1e4 where not), type_vectors [1, W, 1, 1],
    /// marker_map [1, L, 1, 32] (one-hot of each option slot's position).
    func inputs(_ seq: LayaSequence, length: Int, pad: Int) throws -> MLFeatureProvider {
        let L = length, W = width, K = 32
        func array(_ shape: [Int]) throws -> (MLMultiArray, UnsafeMutablePointer<Float16>) {
            let a = try MLMultiArray(shape: shape.map { NSNumber(value: $0) }, dataType: .float16)
            let p = a.dataPointer.bindMemory(to: Float16.self, capacity: a.count)
            p.initialize(repeating: 0, count: a.count)
            return (a, p)
        }
        var ids = seq.ids.map(Int.init)
        let valid = ids.count
        for id in ids where id < 0 || id >= tokens.rows { throw LayaError.model("token id \(id) outside the vocabulary") }
        ids += Array(repeating: pad, count: L - valid)
        let (emb, pe) = try array([1, W, 1, L])
        for t in 0..<L { tokens.row(ids[t], in: file, into: pe.advanced(by: t), stride: L) }
        let (full, pf) = try array([1, L, 1, L])
        let (local, pl) = try array([1, L, 1, L])
        let blocked = Float16(-1e4)
        for key in 0..<L {
            for query in 0..<L {
                let i = key * L + query
                let keyValid = key < valid
                pf[i] = keyValid ? 0 : blocked
                let near = abs(query - key) <= window || query >= valid
                pl[i] = keyValid && near ? 0 : blocked
            }
        }
        let (type, pt) = try array([1, W, 1, 1])
        types.row(Int(seq.kind.index), in: file, into: pt, stride: 1)
        let (markers, pm) = try array([1, L, 1, K])
        for slot in 0..<K {
            let at = slot < seq.markers.count ? Int(seq.markers[slot]) : 0
            pm[at * K + slot] = 1
        }
        return try MLDictionaryFeatureProvider(dictionary: [
            "embeddings": emb, "full_mask": full, "local_mask": local, "type_vectors": type, "marker_map": markers,
        ])
    }
}
