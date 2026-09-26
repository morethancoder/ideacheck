import CoreML
import Foundation

/// Rewrites a checkpoint's enumerated-shape model into fixed-length copies.
///
/// The standard laya-coreml export accepts eleven sequence lengths through
/// Core ML's enumerated shapes. On macOS 26 / iOS 26, Core ML's E5RT runtime
/// fails to plan such a model ("tensor_buffer has known strides while the
/// model has FlexibleShapeInfo") and runs every operation on the CPU: 250 ms
/// for a 32-token question on an M1 Pro, with the GPU idle — Python's
/// laya-coreml included. The same graph pinned to one length plans onto the
/// GPU and runs in 35 ms at 128 tokens.
///
/// So each length is made its own model: the package's protobuf spec is
/// copied with the flexible input dimension fixed (the description's shape
/// and the ML program's input type) and nothing else changed, compiled, and
/// its weights hard-linked back to the checkpoint's one weight file, so a
/// length costs a few megabytes of disk, not 800.
enum FixedShape {
    /// The compiled fixed-length model for `length`, made on first use.
    static func compiled(checkpoint dir: URL, length: Int, stamp: String) throws -> URL {
        let fm = FileManager.default
        let root = dir.appendingPathComponent("fixed", isDirectory: true)
        let target = root.appendingPathComponent("\(length).mlmodelc")
        let stampFile = root.appendingPathComponent("\(length).stamp")
        if fm.fileExists(atPath: target.path), (try? String(contentsOf: stampFile, encoding: .utf8)) == stamp {
            return target
        }
        try fm.createDirectory(at: root, withIntermediateDirectories: true)
        let source = dir.appendingPathComponent("model.mlpackage")
        let data = source.appendingPathComponent("Data/com.apple.CoreML")
        let package = root.appendingPathComponent("\(length)-\(UUID().uuidString).mlpackage")
        defer { try? fm.removeItem(at: package) }
        let pdata = package.appendingPathComponent("Data/com.apple.CoreML")
        try fm.createDirectory(at: pdata.appendingPathComponent("weights"), withIntermediateDirectories: true)
        try fm.copyItem(at: source.appendingPathComponent("Manifest.json"), to: package.appendingPathComponent("Manifest.json"))
        let weights = data.appendingPathComponent("weights/weight.bin")
        try link(weights, pdata.appendingPathComponent("weights/weight.bin"))
        let spec = try [UInt8](Data(contentsOf: data.appendingPathComponent("model.mlmodel")))
        try Data(fix(spec, length: length)).write(to: pdata.appendingPathComponent("model.mlmodel"))

        let temp = try MLModel.compileModel(at: package)
        defer { try? fm.removeItem(at: temp) }
        // The compiler copies the weights; point the copy back at the one file.
        let compiledWeights = temp.appendingPathComponent("weights/weight.bin")
        if fm.fileExists(atPath: compiledWeights.path), size(compiledWeights) == size(weights) {
            try fm.removeItem(at: compiledWeights)
            try link(weights, compiledWeights)
        }
        try? fm.removeItem(at: target)
        try fm.moveItem(at: temp, to: target)
        try stamp.write(to: stampFile, atomically: true, encoding: .utf8)
        return target
    }

    static func size(_ url: URL) -> Int64 {
        ((try? FileManager.default.attributesOfItem(atPath: url.path))?[.size] as? NSNumber)?.int64Value ?? -1
    }

    /// A hard link where the file system allows one, else a copy (APFS
    /// clones it, so still no second copy of the bytes).
    static func link(_ from: URL, _ to: URL) throws {
        do { try FileManager.default.linkItem(at: from, to: to) } catch { try FileManager.default.copyItem(at: from, to: to) }
    }

    // MARK: The spec

    /// Model.proto with every flexible input pinned to `length`:
    /// ModelDescription.input[].type.multiArrayType loses its
    /// enumeratedShapes/shapeRange and takes the fixed shape, and the ML
    /// program's function inputs turn their unknown dimensions constant.
    static func fix(_ spec: [UInt8], length: Int) throws -> [UInt8] {
        var model = try Proto.parse(spec[...])
        var flexible = 0
        for i in model.indices where model[i].number == 2 {  // description
            var desc = try Proto.parse(model[i].bytes[...])
            for j in desc.indices where desc[j].number == 1 {  // input
                var feature = try Proto.parse(desc[j].bytes[...])
                for k in feature.indices where feature[k].number == 3 {  // type
                    var type = try Proto.parse(feature[k].bytes[...])
                    for m in type.indices where type[m].number == 5 {  // multiArrayType
                        var array = try Proto.parse(type[m].bytes[...])
                        guard array.contains(where: { $0.number == 21 || $0.number == 31 }) else { continue }
                        let varying = try varyingDims(array)
                        var shape = try array.filter { $0.number == 1 }.flatMap { try Proto.int64s($0) }
                        for d in varying where d < shape.count { shape[d] = Int64(length) }
                        array.removeAll { $0.number == 1 || $0.number == 21 || $0.number == 31 }
                        array.insert(Proto.Field(number: 1, wire: 2, bytes: Proto.packed(shape)), at: 0)
                        type[m].bytes = Proto.encode(array)
                        flexible += 1
                    }
                    feature[k].bytes = Proto.encode(type)
                }
                desc[j].bytes = Proto.encode(feature)
            }
            model[i].bytes = Proto.encode(desc)
        }
        guard flexible > 0 else { throw LayaError.unsupported("the model has no flexible input to fix") }
        for i in model.indices where model[i].number == 502 {  // mlProgram
            var program = try Proto.parse(model[i].bytes[...])
            for j in program.indices where program[j].number == 2 {  // functions (map entry)
                var entry = try Proto.parse(program[j].bytes[...])
                for k in entry.indices where entry[k].number == 2 {  // Function
                    var function = try Proto.parse(entry[k].bytes[...])
                    for m in function.indices where function[m].number == 1 {  // inputs
                        function[m].bytes = try fixInput(function[m].bytes, length: length)
                    }
                    entry[k].bytes = Proto.encode(function)
                }
                program[j].bytes = Proto.encode(entry)
            }
            model[i].bytes = Proto.encode(program)
        }
        return Proto.encode(model)
    }

    /// NamedValueType → ValueType.tensorType → dimensions: unknown → constant.
    static func fixInput(_ bytes: [UInt8], length: Int) throws -> [UInt8] {
        var named = try Proto.parse(bytes[...])
        for a in named.indices where named[a].number == 2 {
            var value = try Proto.parse(named[a].bytes[...])
            for b in value.indices where value[b].number == 1 {
                var tensor = try Proto.parse(value[b].bytes[...])
                for c in tensor.indices where tensor[c].number == 3 {
                    let dim = try Proto.parse(tensor[c].bytes[...])
                    if dim.contains(where: { $0.number == 2 }) {
                        let constant = Proto.encode([Proto.Field(number: 1, wire: 0, bytes: Proto.varint(UInt64(length)))])
                        tensor[c].bytes = Proto.encode([Proto.Field(number: 1, wire: 2, bytes: constant)])
                    }
                }
                value[b].bytes = Proto.encode(tensor)
            }
            named[a].bytes = Proto.encode(value)
        }
        return Proto.encode(named)
    }

    /// The dimensions that differ among an input's enumerated shapes (a
    /// shape range: the last).
    static func varyingDims(_ array: [Proto.Field]) throws -> [Int] {
        guard let enumerated = array.first(where: { $0.number == 21 }) else {
            let shape = try array.filter { $0.number == 1 }.flatMap { try Proto.int64s($0) }
            return [max(0, shape.count - 1)]
        }
        let shapes = try Proto.parse(enumerated.bytes[...]).filter { $0.number == 1 }.map { s in
            try Proto.parse(s.bytes[...]).filter { $0.number == 1 }.flatMap { try Proto.int64s($0) }
        }
        guard let first = shapes.first else { return [] }
        return first.indices.filter { d in shapes.contains { $0.count > d && $0[d] != first[d] } }
    }
}

/// Just enough of the protobuf wire format to edit a message in place:
/// fields keep their order and bytes unless rewritten.
enum Proto {
    struct Field {
        var number: Int
        var wire: Int
        /// varint: its encoded bytes; length-delimited: the content;
        /// fixed: the 4 or 8 bytes.
        var bytes: [UInt8]
    }

    struct Malformed: Error {}

    static func parse(_ b: ArraySlice<UInt8>) throws -> [Field] {
        var out: [Field] = []
        var i = b.startIndex
        func readVarint() throws -> UInt64 {
            var v: UInt64 = 0, shift: UInt64 = 0
            while true {
                guard i < b.endIndex, shift < 64 else { throw Malformed() }
                let byte = b[i]
                i += 1
                v |= UInt64(byte & 0x7F) << shift
                if byte & 0x80 == 0 { return v }
                shift += 7
            }
        }
        while i < b.endIndex {
            let tag = try readVarint()
            let number = Int(tag >> 3), wire = Int(tag & 7)
            switch wire {
            case 0:
                let start = i
                _ = try readVarint()
                out.append(Field(number: number, wire: 0, bytes: Array(b[start..<i])))
            case 1, 5:
                let n = wire == 1 ? 8 : 4
                guard i + n <= b.endIndex else { throw Malformed() }
                out.append(Field(number: number, wire: wire, bytes: Array(b[i..<i + n])))
                i += n
            case 2:
                let n = Int(try readVarint())
                guard n >= 0, i + n <= b.endIndex else { throw Malformed() }
                out.append(Field(number: number, wire: 2, bytes: Array(b[i..<i + n])))
                i += n
            default:
                throw Malformed()
            }
        }
        return out
    }

    static func encode(_ fields: [Field]) -> [UInt8] {
        var out: [UInt8] = []
        for f in fields {
            out += varint(UInt64(f.number << 3 | f.wire))
            if f.wire == 2 { out += varint(UInt64(f.bytes.count)) }
            out += f.bytes
        }
        return out
    }

    static func varint(_ v: UInt64) -> [UInt8] {
        var v = v
        var out: [UInt8] = []
        repeat {
            var byte = UInt8(v & 0x7F)
            v >>= 7
            if v != 0 { byte |= 0x80 }
            out.append(byte)
        } while v != 0
        return out
    }

    /// A repeated int64, packed or not.
    static func int64s(_ f: Field) throws -> [Int64] {
        if f.wire == 0 { return [try decodeVarints(f.bytes[...]).first ?? 0].map { Int64(bitPattern: $0) } }
        return try decodeVarints(f.bytes[...]).map { Int64(bitPattern: $0) }
    }

    static func decodeVarints(_ b: ArraySlice<UInt8>) throws -> [UInt64] {
        var out: [UInt64] = []
        var v: UInt64 = 0, shift: UInt64 = 0
        for byte in b {
            guard shift < 64 else { throw Malformed() }
            v |= UInt64(byte & 0x7F) << shift
            if byte & 0x80 == 0 {
                out.append(v)
                v = 0
                shift = 0
            } else {
                shift += 7
            }
        }
        return out
    }

    static func packed(_ values: [Int64]) -> [UInt8] {
        values.flatMap { varint(UInt64(bitPattern: $0)) }
    }
}
