import DeviceCheck
import Foundation
import Security

/// A small string store: the Keychain in the app, a dictionary in tests.
protocol SecretStore: Sendable {
    func read(_ account: String) -> String?
    func write(_ value: String, for account: String)
    func delete(_ account: String)
}

/// Generic-password items under one service. The user id is kept
/// `AfterFirstUnlock` (it follows an encrypted backup to a new phone, and
/// iOS keeps it when the app is deleted and installed again); the App Attest
/// key id is `ThisDeviceOnly`, because the key itself never leaves the device.
struct KeychainStore: SecretStore {
    var service = "com.morethancoder.sparkjudge"

    func read(_ account: String) -> String? {
        var query = base(account)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &out) == errSecSuccess, let data = out as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    func write(_ value: String, for account: String) {
        let data = Data(value.utf8)
        let accessible = account.hasPrefix(AttestKeyID.prefix)
            ? kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly : kSecAttrAccessibleAfterFirstUnlock
        let update: [String: Any] = [kSecValueData as String: data, kSecAttrAccessible as String: accessible]
        if SecItemUpdate(base(account) as CFDictionary, update as CFDictionary) == errSecItemNotFound {
            var add = base(account)
            add.merge(update) { $1 }
            SecItemAdd(add as CFDictionary, nil)
        }
    }

    func delete(_ account: String) {
        SecItemDelete(base(account) as CFDictionary)
    }

    private func base(_ account: String) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service,
         kSecAttrAccount as String: account]
    }
}

/// An in-memory store, for tests and previews.
final class MemoryStore: SecretStore, @unchecked Sendable {
    private let lock = NSLock()
    private var values: [String: String]

    init(_ values: [String: String] = [:]) { self.values = values }

    func read(_ account: String) -> String? { lock.withLock { values[account] } }
    func write(_ value: String, for account: String) { lock.withLock { values[account] = value } }
    func delete(_ account: String) { lock.withLock { values[account] = nil } }
}

/// The app's user id: a UUID made once and kept in the Keychain. It is the
/// hosted API's `X-App-User-Id` and RevenueCat's `appUserID`, so a purchase and
/// a check name the same person.
enum AppUserID {
    static let account = "app-user-id"

    static func current(in store: any SecretStore = KeychainStore()) -> String {
        if let id = store.read(account), UUID(uuidString: id) != nil { return id }
        let id = UUID().uuidString.lowercased()
        store.write(id, for: account)
        return id
    }
}

/// Where an attested key's id is kept between launches: one per server, since
/// a key is attested with the server that issued its challenge.
enum AttestKeyID {
    static let prefix = "app-attest-key-id"

    static func account(for baseURL: URL) -> String {
        "\(prefix)@\(baseURL.host(percentEncoded: false) ?? "")"
    }
}

/// App Attest, behind a protocol so the client can be tested with a fake.
protocol AttestService: Sendable {
    var isSupported: Bool { get }
    func generateKey() async throws -> String
    func attestKey(_ keyID: String, clientDataHash: Data) async throws -> Data
    func generateAssertion(_ keyID: String, clientDataHash: Data) async throws -> Data
}

/// Apple's `DCAppAttestService`. `isSupported` is false in the simulator.
struct DeviceAttestService: AttestService {
    var isSupported: Bool { DCAppAttestService.shared.isSupported }

    func generateKey() async throws -> String {
        try await DCAppAttestService.shared.generateKey()
    }

    func attestKey(_ keyID: String, clientDataHash: Data) async throws -> Data {
        try await DCAppAttestService.shared.attestKey(keyID, clientDataHash: clientDataHash)
    }

    func generateAssertion(_ keyID: String, clientDataHash: Data) async throws -> Data {
        try await DCAppAttestService.shared.generateAssertion(keyID, clientDataHash: clientDataHash)
    }
}
