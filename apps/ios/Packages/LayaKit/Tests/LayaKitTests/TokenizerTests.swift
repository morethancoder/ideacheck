import Foundation
import Testing
@testable import LayaKit

/// Token ids against the Python `tokenizers` package: Fixtures/tokenizer_ids.json
/// was made with tokenizers 0.23 over each checkpoint's tokenizer.json (see
/// scripts/fixtures.py); the comparison runs where that tokenizer.json is on
/// disk (scripts/fetch-model.sh --tokenizer <repo>).
struct TokenizerTests {
    static let checkpoints = ["laya-typed-decisions-coreml", "laya-multilingual-coreml-ane"]

    @Test(arguments: checkpoints)
    func idsMatchPython(checkpoint: String) throws {
        try #require(Local.hasTokenizer(checkpoint), "no tokenizer for \(checkpoint): scripts/fetch-model.sh --tokenizer aac6fef/\(checkpoint)")
        let fixture = try #require(try Fixture.json("tokenizer_ids.json")[checkpoint])
        let tok = try Local.tokenizer(checkpoint)
        let specials = try #require(fixture["specials"])
        #expect(Self.int(specials["cls"]) == tok.clsID)
        #expect(Self.int(specials["sep"]) == tok.sepID)
        #expect(Self.int(specials["pad"]) == tok.padID)
        #expect(Self.int(specials["mask"]) == tok.maskID)
        #expect(specials["mask_token"]?.stringValue == tok.maskToken)
        for c in fixture["cases"]?.arrayValue ?? [] {
            let text = try #require(c["text"]?.stringValue)
            let want = (c["ids"]?.arrayValue ?? []).map { Self.int($0) }
            #expect(tok.encode(text) == want, "\(checkpoint): \(text.debugDescription)")
        }
    }

    static func int(_ v: JSONValue?) -> Int {
        if case .number(let n) = v { return Int(n) ?? -1 }
        return -1
    }

    /// The GPT-2 split, which the byte-level tokenizers run before BPE.
    @Test func byteLevelSplit() {
        func words(_ s: String) -> [String] {
            Tokenizer.byteLevelWords(Array(s.unicodeScalars)).map { String(String.UnicodeScalarView($0)) }
        }
        #expect(words("Hello, world!") == ["Hello", ",", " world", "!"])
        #expect(words("don't stop") == ["don", "'t", " stop"])
        #expect(words("a   b") == ["a", "  ", " b"])
        #expect(words("x\n\ny") == ["x", "\n", "\n", "y"])
        #expect(words("end  ") == ["end", "  "])
        #expect(words("42 apples") == ["42", " apples"])
        #expect(words("!'s") == ["!'", "s"])
    }

    /// BPE mechanics on a tiny vocabulary: merges apply lowest rank first,
    /// added tokens are cut out before, and lstrip swallows the space before.
    @Test func tinyByteLevel() throws {
        let vocab: [String: Int] = ["a": 0, "b": 1, "c": 2, "Ġ": 3, "ab": 4, "bc": 5, "abc": 6, "Ġa": 7, "[CLS]": 8, "[SEP]": 9, "[PAD]": 10, "[MASK]": 11]
        let json: [String: Any] = [
            "normalizer": ["type": "NFC"],
            "pre_tokenizer": ["type": "ByteLevel", "add_prefix_space": false, "use_regex": true],
            "added_tokens": [
                ["id": 8, "content": "[CLS]", "special": true, "normalized": false],
                ["id": 9, "content": "[SEP]", "special": true, "normalized": false],
                ["id": 10, "content": "[PAD]", "special": true, "normalized": false],
                ["id": 11, "content": "[MASK]", "special": true, "normalized": false, "lstrip": true],
            ],
            "model": ["type": "BPE", "vocab": vocab, "merges": [["b", "c"], ["a", "b"], ["ab", "c"], ["Ġ", "a"]]],
        ]
        let config: [String: Any] = ["cls_token": "[CLS]", "sep_token": "[SEP]", "pad_token": "[PAD]", "mask_token": "[MASK]"]
        let tok = try Tokenizer(tokenizerJSON: JSONSerialization.data(withJSONObject: json), configJSON: JSONSerialization.data(withJSONObject: config))
        // "abc": b+c (rank 0) first, so a + bc, which has no merge.
        #expect(tok.encode("abc") == [0, 5])
        #expect(tok.encode("ab") == [4])
        #expect(tok.encode(" a") == [7])
        #expect(tok.encode("ab [MASK]c") == [4, 11, 2])
        #expect(tok.encode("[CLS]ab[SEP]") == [8, 4, 9])
    }
}
