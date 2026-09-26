import Foundation

/// A JSON value that keeps object keys in the order they came in.
///
/// Laya reads a structured state as the text Python's `json.dumps(state,
/// ensure_ascii=False)` makes of it, key order included, so the state has to
/// survive the trip from the request to the prompt exactly: Foundation's
/// JSONSerialization would reorder keys and reformat numbers.
public indirect enum JSONValue: Equatable, Sendable {
    case null
    case bool(Bool)
    /// The number as written: Python reprints the float it parsed, which for
    /// the shortest-form numbers Go and Swift write is the same text.
    case number(String)
    case string(String)
    case array([JSONValue])
    case object([(String, JSONValue)])

    public static func == (a: JSONValue, b: JSONValue) -> Bool {
        switch (a, b) {
        case (.null, .null): true
        case (.bool(let x), .bool(let y)): x == y
        case (.number(let x), .number(let y)): x == y
        case (.string(let x), .string(let y)): x == y
        case (.array(let x), .array(let y)): x == y
        case (.object(let x), .object(let y)): x.count == y.count && zip(x, y).allSatisfy { $0.0 == $1.0 && $0.1 == $1.1 }
        default: false
        }
    }

    public subscript(key: String) -> JSONValue? {
        if case .object(let pairs) = self { return pairs.first { $0.0 == key }?.1 }
        return nil
    }

    public var stringValue: String? {
        if case .string(let s) = self { return s }
        return nil
    }

    public var boolValue: Bool? {
        if case .bool(let b) = self { return b }
        return nil
    }

    public var intValue: Int? {
        if case .number(let n) = self { return Int(n) ?? Double(n).map { Int($0) } }
        return nil
    }

    public var doubleValue: Double? {
        if case .number(let n) = self { return Double(n) }
        return nil
    }

    public var arrayValue: [JSONValue]? {
        if case .array(let a) = self { return a }
        return nil
    }

    public var objectValue: [(String, JSONValue)]? {
        if case .object(let o) = self { return o }
        return nil
    }

    // MARK: Python-compatible text

    /// `json.dumps(value, ensure_ascii=False)`: ", " and ": " separators,
    /// non-ASCII left as it is.
    public var pythonJSON: String { pythonJSON(ensureASCII: false) }

    /// `json.dumps(value, ensure_ascii=…)`.
    public func pythonJSON(ensureASCII: Bool) -> String {
        var out = ""
        write(to: &out, ascii: ensureASCII)
        return out
    }

    func write(to out: inout String, ascii: Bool) {
        switch self {
        case .null: out += "null"
        case .bool(let b): out += b ? "true" : "false"
        case .number(let n): out += n
        case .string(let s): Self.quote(s, into: &out, ascii: ascii)
        case .array(let items):
            out += "["
            for (i, item) in items.enumerated() {
                if i > 0 { out += ", " }
                item.write(to: &out, ascii: ascii)
            }
            out += "]"
        case .object(let pairs):
            out += "{"
            for (i, (key, value)) in pairs.enumerated() {
                if i > 0 { out += ", " }
                Self.quote(key, into: &out, ascii: ascii)
                out += ": "
                value.write(to: &out, ascii: ascii)
            }
            out += "}"
        }
    }

    static func quote(_ s: String, into out: inout String, ascii: Bool) {
        out += "\""
        for scalar in s.unicodeScalars {
            switch scalar {
            case "\"": out += "\\\""
            case "\\": out += "\\\\"
            case "\n": out += "\\n"
            case "\r": out += "\\r"
            case "\t": out += "\\t"
            case "\u{08}": out += "\\b"
            case "\u{0C}": out += "\\f"
            default:
                if scalar.value < 0x20 || (ascii && scalar.value > 0x7E) {
                    for unit in String(scalar).utf16 { out += String(format: "\\u%04x", unit) }
                } else {
                    out.unicodeScalars.append(scalar)
                }
            }
        }
        out += "\""
    }

    // MARK: Parsing

    public init(parsing text: String) throws {
        var parser = Parser(bytes: Array(text.utf8))
        self = try parser.document()
    }

    public init(parsing data: Data) throws {
        var parser = Parser(bytes: [UInt8](data))
        self = try parser.document()
    }

    public struct ParseError: Error, CustomStringConvertible {
        public let offset: Int
        public let why: String
        public var description: String { "bad JSON at byte \(offset): \(why)" }
    }

    struct Parser {
        let bytes: [UInt8]
        var i = 0

        init(bytes: [UInt8]) { self.bytes = bytes }

        mutating func document() throws -> JSONValue {
            let v = try value()
            skip()
            if i != bytes.count { throw ParseError(offset: i, why: "trailing data") }
            return v
        }

        mutating func skip() {
            while i < bytes.count, [0x20, 0x09, 0x0A, 0x0D].contains(bytes[i]) { i += 1 }
        }

        mutating func value() throws -> JSONValue {
            skip()
            guard i < bytes.count else { throw ParseError(offset: i, why: "unexpected end") }
            switch bytes[i] {
            case UInt8(ascii: "{"):
                i += 1
                var pairs: [(String, JSONValue)] = []
                skip()
                if i < bytes.count, bytes[i] == UInt8(ascii: "}") { i += 1; return .object(pairs) }
                while true {
                    skip()
                    let key = try string()
                    skip()
                    try expect(":")
                    pairs.append((key, try value()))
                    skip()
                    if try next(",", or: "}") { continue }
                    return .object(pairs)
                }
            case UInt8(ascii: "["):
                i += 1
                var items: [JSONValue] = []
                skip()
                if i < bytes.count, bytes[i] == UInt8(ascii: "]") { i += 1; return .array(items) }
                while true {
                    items.append(try value())
                    skip()
                    if try next(",", or: "]") { continue }
                    return .array(items)
                }
            case UInt8(ascii: "\""):
                return .string(try string())
            case UInt8(ascii: "t"): try literal("true"); return .bool(true)
            case UInt8(ascii: "f"): try literal("false"); return .bool(false)
            case UInt8(ascii: "n"): try literal("null"); return .null
            default:
                let start = i
                while i < bytes.count, "+-0123456789.eE".utf8.contains(bytes[i]) { i += 1 }
                guard i > start else { throw ParseError(offset: i, why: "unexpected character") }
                return .number(String(decoding: bytes[start..<i], as: UTF8.self))
            }
        }

        mutating func expect(_ c: Character) throws {
            guard i < bytes.count, bytes[i] == c.asciiValue! else { throw ParseError(offset: i, why: "expected \(c)") }
            i += 1
        }

        /// true on `more`, false on `end`.
        mutating func next(_ more: Character, or end: Character) throws -> Bool {
            guard i < bytes.count else { throw ParseError(offset: i, why: "unexpected end") }
            if bytes[i] == more.asciiValue! { i += 1; return true }
            if bytes[i] == end.asciiValue! { i += 1; return false }
            throw ParseError(offset: i, why: "expected \(more) or \(end)")
        }

        mutating func literal(_ word: String) throws {
            let w = Array(word.utf8)
            guard i + w.count <= bytes.count, Array(bytes[i..<i + w.count]) == w else {
                throw ParseError(offset: i, why: "expected \(word)")
            }
            i += w.count
        }

        mutating func string() throws -> String {
            try expect("\"")
            var out = [UInt8]()
            while i < bytes.count {
                let b = bytes[i]
                i += 1
                if b == UInt8(ascii: "\"") { return String(decoding: out, as: UTF8.self) }
                if b != UInt8(ascii: "\\") { out.append(b); continue }
                guard i < bytes.count else { break }
                let e = bytes[i]
                i += 1
                switch e {
                case UInt8(ascii: "n"): out.append(0x0A)
                case UInt8(ascii: "r"): out.append(0x0D)
                case UInt8(ascii: "t"): out.append(0x09)
                case UInt8(ascii: "b"): out.append(0x08)
                case UInt8(ascii: "f"): out.append(0x0C)
                case UInt8(ascii: "u"):
                    var code = try hex4()
                    if (0xD800..<0xDC00).contains(code), i + 1 < bytes.count,
                        bytes[i] == UInt8(ascii: "\\"), bytes[i + 1] == UInt8(ascii: "u")
                    {
                        i += 2
                        let low = try hex4()
                        code = 0x10000 + ((code - 0xD800) << 10) + (low - 0xDC00)
                    }
                    let scalar = Unicode.Scalar(code) ?? "\u{FFFD}"
                    out.append(contentsOf: Array(String(scalar).utf8))
                default: out.append(e)
                }
            }
            throw ParseError(offset: i, why: "unterminated string")
        }

        mutating func hex4() throws -> UInt32 {
            guard i + 4 <= bytes.count, let v = UInt32(String(decoding: bytes[i..<i + 4], as: UTF8.self), radix: 16) else {
                throw ParseError(offset: i, why: "bad \\u escape")
            }
            i += 4
            return v
        }
    }
}
