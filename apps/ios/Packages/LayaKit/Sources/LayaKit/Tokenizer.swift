import Foundation

/// The BPE tokenizer a Laya checkpoint ships in `tokenizer/tokenizer.json`,
/// reimplemented from Hugging Face `tokenizers` semantics so that it yields the
/// same ids — no Python, no dependency. Two families are covered, the two the
/// published checkpoints use:
///
/// - ModernBERT (laya-coreml, laya-typed-decisions-coreml): NFC, then GPT-2
///   byte-level pre-tokenization and BPE over the byte alphabet.
/// - Gemma (laya-multilingual-*): spaces become "▁", Metaspace splitting, BPE
///   with byte fallback.
///
/// In both, added tokens found in the text (`[CLS]`, runs of spaces, …) are
/// cut out first, as `tokenizers` does: the non-normalized ones on the raw
/// text, the normalized ones after normalizing, leftmost-longest.
public final class Tokenizer: @unchecked Sendable {
    enum Normalizer {
        case none, nfc
        case replace(String, String)
    }

    enum PreTokenizer {
        case byteLevel
        case metaspace(replacement: Character, prependAlways: Bool, split: Bool)
    }

    struct AddedToken {
        let content: [Unicode.Scalar]
        let id: Int
        let lstrip: Bool
        let rstrip: Bool
        let normalized: Bool
    }

    let normalizer: Normalizer
    let preTokenizer: PreTokenizer
    /// Keyed by the token's exact bytes: a Swift String key would compare
    /// by canonical equivalence and fold distinct tokens together (Å and the
    /// Angstrom sign, CJK compatibility ideographs, é and e + U+0301).
    let vocab: [Exact: Int]
    /// (left id, right id) → (rank, merged id)
    let merges: [UInt64: (rank: Int32, id: Int32)]
    let byteFallback: Bool
    let unkID: Int?
    let fuseUnk: Bool
    let rawAdded: [AddedToken]
    let normalizedAdded: [AddedToken]
    private var cache: [Exact: [Int]] = [:]
    private let lock = NSLock()

    public let clsID: Int, sepID: Int, padID: Int, maskID: Int
    public let maskToken: String

    /// Loads `dir/tokenizer.json` and the special tokens named in
    /// `dir/tokenizer_config.json` (cls, sep, pad, mask), as laya-coreml does.
    public convenience init(directory: URL) throws {
        let data = try Data(contentsOf: directory.appendingPathComponent("tokenizer.json"))
        let config = try Data(contentsOf: directory.appendingPathComponent("tokenizer_config.json"))
        try self.init(tokenizerJSON: data, configJSON: config)
    }

    public init(tokenizerJSON: Data, configJSON: Data) throws {
        // Parsed byte-exact: Foundation's NSString keys treat some distinct
        // tokens as equal ("\u{FEFF}\r" and "\r"), which folds them together.
        let root = try JSONValue(parsing: tokenizerJSON)
        guard let model = root["model"], model["type"]?.stringValue == "BPE", let table = model["vocab"]?.objectValue else {
            throw LayaError.unsupported("tokenizer.json is not a BPE tokenizer")
        }
        var vocab: [Exact: Int] = [:]
        vocab.reserveCapacity(table.count)
        for (key, value) in table {
            if let id = value.intValue { vocab[Exact(key)] = id }
        }
        self.vocab = vocab

        let norm = root["normalizer"]
        switch norm?["type"]?.stringValue {
        case nil: normalizer = .none
        case "NFC": normalizer = .nfc
        case "Replace":
            guard let from = norm?["pattern"]?["String"]?.stringValue, let to = norm?["content"]?.stringValue else {
                throw LayaError.unsupported("Replace normalizer without a string pattern")
            }
            normalizer = .replace(from, to)
        case let other: throw LayaError.unsupported("normalizer \(other ?? "?")")
        }

        let pre = root["pre_tokenizer"]
        switch pre?["type"]?.stringValue {
        case "ByteLevel":
            guard pre?["add_prefix_space"]?.boolValue != true, pre?["use_regex"]?.boolValue != false else {
                throw LayaError.unsupported("ByteLevel with add_prefix_space or without its regex")
            }
            preTokenizer = .byteLevel
        case "Metaspace":
            let rep = pre?["replacement"]?.stringValue?.first ?? "▁"
            let scheme = pre?["prepend_scheme"]?.stringValue ?? "always"
            guard scheme == "always" || scheme == "never" else { throw LayaError.unsupported("Metaspace prepend_scheme \(scheme)") }
            preTokenizer = .metaspace(replacement: rep, prependAlways: scheme == "always", split: pre?["split"]?.boolValue ?? true)
        case let other: throw LayaError.unsupported("pre_tokenizer \(other ?? "none")")
        }

        byteFallback = model["byte_fallback"]?.boolValue ?? false
        fuseUnk = model["fuse_unk"]?.boolValue ?? false
        unkID = model["unk_token"]?.stringValue.flatMap { vocab[Exact($0)] }
        if model["continuing_subword_prefix"]?.stringValue != nil || model["end_of_word_suffix"]?.stringValue != nil {
            throw LayaError.unsupported("BPE with subword prefixes or suffixes")
        }

        var merges: [UInt64: (rank: Int32, id: Int32)] = [:]
        let list = model["merges"]?.arrayValue ?? []
        merges.reserveCapacity(list.count)
        for (rank, entry) in list.enumerated() {
            let pair: (String, String)
            if let a = entry.arrayValue, a.count == 2, let l = a[0].stringValue, let r = a[1].stringValue {
                pair = (l, r)
            } else if let s = entry.stringValue, let space = s.unicodeScalars.firstIndex(of: " ") {
                pair = (String(s.unicodeScalars[..<space]), String(s.unicodeScalars[s.unicodeScalars.index(after: space)...]))
            } else { continue }
            guard let l = vocab[Exact(pair.0)], let r = vocab[Exact(pair.1)], let m = vocab[Exact(pair.0 + pair.1)] else { continue }
            let key = UInt64(UInt32(l)) << 32 | UInt64(UInt32(r))
            if merges[key] == nil { merges[key] = (Int32(rank), Int32(m)) }
        }
        self.merges = merges

        var raw: [AddedToken] = [], normalized: [AddedToken] = []
        for t in root["added_tokens"]?.arrayValue ?? [] {
            guard let content = t["content"]?.stringValue, let id = t["id"]?.intValue, !content.isEmpty else { continue }
            let token = AddedToken(
                content: Array(content.unicodeScalars), id: id, lstrip: t["lstrip"]?.boolValue ?? false,
                rstrip: t["rstrip"]?.boolValue ?? false, normalized: t["normalized"]?.boolValue ?? !(t["special"]?.boolValue ?? false))
            if token.normalized { normalized.append(token) } else { raw.append(token) }
        }
        rawAdded = raw
        normalizedAdded = normalized

        guard let cfg = try JSONSerialization.jsonObject(with: configJSON) as? [String: Any] else {
            throw LayaError.unsupported("tokenizer_config.json")
        }
        var addedByContent: [Exact: Int] = [:]
        for t in raw + normalized { addedByContent[Exact(String(String.UnicodeScalarView(t.content)))] = t.id }
        func special(_ name: String) throws -> (String, Int) {
            var value = cfg[name]
            if let d = value as? [String: Any] { value = d["content"] }
            guard let s = value as? String, let id = addedByContent[Exact(s)] ?? vocab[Exact(s)] else {
                throw LayaError.unsupported("tokenizer is missing a valid \(name)")
            }
            return (s, id)
        }
        clsID = try special("cls_token").1
        sepID = try special("sep_token").1
        padID = try special("pad_token").1
        (maskToken, maskID) = try special("mask_token")
    }

    /// Token ids for `text`, without [CLS]/[SEP] (add_special_tokens=False).
    public func encode(_ text: String) -> [Int] {
        var ids: [Int] = []
        for piece in split(Array(text.unicodeScalars), on: rawAdded) {
            switch piece {
            case .token(let id): ids.append(id)
            case .text(let raw):
                let normal = normalize(raw)
                for inner in split(normal, on: normalizedAdded) {
                    switch inner {
                    case .token(let id): ids.append(id)
                    case .text(let t): ids.append(contentsOf: model(t))
                    }
                }
            }
        }
        return ids
    }

    enum Piece {
        case token(Int)
        case text([Unicode.Scalar])
    }

    /// Cuts the added tokens out of text, leftmost-longest, widening a match
    /// over the whitespace its lstrip/rstrip swallows.
    func split(_ s: [Unicode.Scalar], on tokens: [AddedToken]) -> [Piece] {
        if tokens.isEmpty || s.isEmpty { return s.isEmpty ? [] : [.text(s)] }
        var out: [Piece] = []
        var start = 0, i = 0
        while i < s.count {
            var best: AddedToken?
            for t in tokens where t.content.count > (best?.content.count ?? 0) && i + t.content.count <= s.count {
                if t.content.first == s[i], Array(s[i..<i + t.content.count]) == t.content { best = t }
            }
            guard let t = best else { i += 1; continue }
            var from = i, to = i + t.content.count
            if t.lstrip { while from > start, s[from - 1].properties.isWhitespace { from -= 1 } }
            if t.rstrip { while to < s.count, s[to].properties.isWhitespace { to += 1 } }
            if from > start { out.append(.text(Array(s[start..<from]))) }
            out.append(.token(t.id))
            start = to
            i = to
        }
        if start < s.count { out.append(.text(Array(s[start...]))) }
        return out
    }

    func normalize(_ s: [Unicode.Scalar]) -> [Unicode.Scalar] {
        switch normalizer {
        case .none: return s
        case .nfc: return Array(String(String.UnicodeScalarView(s)).precomposedStringWithCanonicalMapping.unicodeScalars)
        case .replace(let from, let to):
            let f = Array(from.unicodeScalars), t = Array(to.unicodeScalars)
            var out: [Unicode.Scalar] = []
            var i = 0
            while i < s.count {
                if i + f.count <= s.count, Array(s[i..<i + f.count]) == f {
                    out += t
                    i += f.count
                } else {
                    out.append(s[i])
                    i += 1
                }
            }
            return out
        }
    }

    /// Pre-tokenizes one stretch of text and runs BPE on each word.
    func model(_ s: [Unicode.Scalar]) -> [Int] {
        var ids: [Int] = []
        switch preTokenizer {
        case .byteLevel:
            for word in Self.byteLevelWords(s) {
                var mapped = ""
                for b in String(String.UnicodeScalarView(word)).utf8 { mapped.unicodeScalars.append(Self.byteToUnicode[Int(b)]) }
                ids += bpe(mapped)
            }
        case .metaspace(let rep, let prepend, let split):
            let r = rep.unicodeScalars.first!
            var t = s.map { $0 == " " ? r : $0 }
            if prepend, t.first != r { t.insert(r, at: 0) }
            var words: [[Unicode.Scalar]] = []
            if split {
                var cur: [Unicode.Scalar] = []
                for c in t {
                    if c == r, !cur.isEmpty { words.append(cur); cur = [] }
                    cur.append(c)
                }
                if !cur.isEmpty { words.append(cur) }
            } else {
                words = [t]
            }
            for w in words { ids += bpe(String(String.UnicodeScalarView(w))) }
        }
        return ids
    }

    /// BPE over one pre-tokenized word: start from its characters (or their
    /// bytes, <0xNN>, when a character is not in the vocabulary), then apply
    /// the lowest-ranked merge, leftmost first, until none applies.
    func bpe(_ word: String) -> [Int] {
        let key = Exact(word)
        lock.lock()
        if let hit = cache[key] { lock.unlock(); return hit }
        lock.unlock()
        var symbols: [Int] = []
        var lastUnk = false
        for scalar in word.unicodeScalars {
            if let id = vocab[Exact(String(scalar))] {
                symbols.append(id)
                lastUnk = false
            } else if byteFallback, case let bytes = Array(String(scalar).utf8),
                case let fb = bytes.compactMap({ vocab[Exact(String(format: "<0x%02X>", $0))] }), fb.count == bytes.count
            {
                symbols += fb
                lastUnk = false
            } else if let unk = unkID {
                if !(fuseUnk && lastUnk) { symbols.append(unk) }
                lastUnk = true
            }
        }
        while symbols.count > 1 {
            var best: (rank: Int32, id: Int32, at: Int)?
            for i in 0..<symbols.count - 1 {
                let key = UInt64(UInt32(symbols[i])) << 32 | UInt64(UInt32(symbols[i + 1]))
                if let m = merges[key], m.rank < (best?.rank ?? .max) { best = (m.rank, m.id, i) }
            }
            guard let b = best else { break }
            symbols[b.at] = Int(b.id)
            symbols.remove(at: b.at + 1)
        }
        lock.lock()
        if cache.count > 50_000 { cache.removeAll() }
        cache[key] = symbols
        lock.unlock()
        return symbols
    }

    // MARK: Byte level

    /// GPT-2's reversible byte → printable character map.
    static let byteToUnicode: [Unicode.Scalar] = {
        var bs = Array(33...126) + Array(161...172) + Array(174...255)
        var cs = bs
        var n = 0
        for b in 0..<256 where !bs.contains(b) {
            bs.append(b)
            cs.append(256 + n)
            n += 1
        }
        var map = [Unicode.Scalar](repeating: " ", count: 256)
        for (b, c) in zip(bs, cs) { map[b] = Unicode.Scalar(UInt32(c))! }
        return map
    }()

    enum Class { case letter, number, space, other }

    static func classify(_ c: Unicode.Scalar) -> Class {
        if c.properties.isWhitespace { return .space }
        switch c.properties.generalCategory {
        case .uppercaseLetter, .lowercaseLetter, .titlecaseLetter, .modifierLetter, .otherLetter: return .letter
        case .decimalNumber, .letterNumber, .otherNumber: return .number
        default: return .other
        }
    }

    /// GPT-2's pre-tokenizer regex, by hand:
    /// 's|'t|'re|'ve|'m|'ll|'d| ?\p{L}+| ?\p{N}+| ?[^\s\p{L}\p{N}]+|\s+(?!\S)|\s+
    static func byteLevelWords(_ s: [Unicode.Scalar]) -> [[Unicode.Scalar]] {
        var words: [[Unicode.Scalar]] = []
        var i = 0
        let n = s.count
        func run(from j: Int, _ cls: Class) -> Int {
            var k = j
            while k < n, classify(s[k]) == cls { k += 1 }
            return k
        }
        while i < n {
            let c = s[i]
            if c == "'", i + 1 < n {
                let a = s[i + 1]
                if "stmd".unicodeScalars.contains(a) {
                    words.append([c, a]); i += 2; continue
                }
                if i + 2 < n {
                    let pair = String(String.UnicodeScalarView([a, s[i + 2]]))
                    if ["re", "ve", "ll"].contains(pair) {
                        words.append(Array(s[i..<i + 3])); i += 3; continue
                    }
                }
            }
            var start = i
            if c == " ", i + 1 < n, classify(s[i + 1]) != .space { start = i + 1 }
            let cls = classify(s[start])
            if cls != .space {
                let end = run(from: start, cls)
                words.append(Array(s[i..<end]))
                i = end
                continue
            }
            // Whitespace: the run, less its last character when a
            // non-space follows (that one goes alone, or with the next word
            // if it is a plain space).
            let end = run(from: i, .space)
            if end == n {
                words.append(Array(s[i..<end]))
                i = end
            } else if end - i >= 2 {
                words.append(Array(s[i..<end - 1]))
                i = end - 1
            } else {
                words.append([c])
                i += 1
            }
        }
        return words
    }
}

/// A string compared by its bytes, not by canonical equivalence.
struct Exact: Hashable {
    let bytes: [UInt8]
    init(_ s: String) { bytes = Array(s.utf8) }
}

public enum LayaError: LocalizedError, Equatable {
    case unsupported(String)
    case badQuestion(String)
    case capacity(String)
    case model(String)
    case download(String)

    public var errorDescription: String? {
        switch self {
        case .unsupported(let why): "unsupported checkpoint: \(why)"
        case .badQuestion(let why): "bad question: \(why)"
        case .capacity(let why): "does not fit the model: \(why)"
        case .model(let why): "laya: \(why)"
        case .download(let why): "download: \(why)"
        }
    }
}
