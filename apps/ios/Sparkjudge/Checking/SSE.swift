import Foundation

/// One Server-Sent Event.
struct SSEMessage: Sendable, Equatable {
    var event: String
    var data: String
}

/// A line-at-a-time Server-Sent Events parser (the subset ideacheck's server
/// writes: `event:` and `data:` lines, a blank line ends a message). Pure, so
/// the tests can feed it any chunking.
struct SSEParser: Sendable {
    private var event = ""
    private var data: [String] = []

    /// Feeds one line without its terminator; returns a message when the line completes one.
    mutating func feed(line raw: String) -> SSEMessage? {
        let line = raw.hasSuffix("\r") ? String(raw.dropLast()) : raw
        if line.isEmpty {
            defer { event = ""; data = [] }
            guard !data.isEmpty else { return nil }
            return SSEMessage(event: event.isEmpty ? "message" : event, data: data.joined(separator: "\n"))
        }
        if line.hasPrefix(":") { return nil } // comment / keep-alive
        let (field, value) = Self.split(line)
        switch field {
        case "event": event = value
        case "data": data.append(value)
        default: break // id, retry: not used by ideacheck
        }
        return nil
    }

    /// Flushes a final message whose trailing blank line never came.
    mutating func finish() -> SSEMessage? { feed(line: "") }

    private static func split(_ line: String) -> (String, String) {
        guard let colon = line.firstIndex(of: ":") else { return (line, "") }
        var value = line[line.index(after: colon)...]
        if value.hasPrefix(" ") { value = value.dropFirst() }
        return (String(line[..<colon]), String(value))
    }
}

/// Splits a byte stream into lines, keeping empty ones (which `AsyncBytes.lines`
/// drops, and which SSE needs to end a message).
struct LineSplitter: Sendable {
    private var buffer: [UInt8] = []

    mutating func push(_ byte: UInt8) -> String? {
        if byte == UInt8(ascii: "\n") {
            defer { buffer.removeAll(keepingCapacity: true) }
            return String(decoding: buffer, as: UTF8.self)
        }
        buffer.append(byte)
        return nil
    }

    mutating func rest() -> String? {
        guard !buffer.isEmpty else { return nil }
        defer { buffer.removeAll() }
        return String(decoding: buffer, as: UTF8.self)
    }
}
