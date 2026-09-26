import Foundation
import Testing
@testable import Sparkjudge

struct CardStyleTests {
    @Test func sameSeedGivesSameStyle() {
        for seed: UInt64 in [0, 1, 42, .max, 0xDEAD_BEEF] {
            #expect(CardStyle.make(seed: seed) == CardStyle.make(seed: seed))
        }
    }

    @Test func seedFromUUIDIsStable() {
        let id = UUID(uuidString: "6F9619FF-8B86-D011-B42D-00C04FC964FF")!
        #expect(CardStyle.seed(for: id) == CardStyle.seed(for: id))
        // Pinned: FNV-1a over the UUID bytes must not change between releases,
        // or every saved card would change its look.
        #expect(CardStyle.seed(for: id) == FNV1a.hash([0x6F, 0x96, 0x19, 0xFF, 0x8B, 0x86, 0xD0, 0x11, 0xB4, 0x2D, 0x00, 0xC0, 0x4F, 0xC9, 0x64, 0xFF]))
        #expect(FNV1a.hash("") == 0xcbf2_9ce4_8422_2325)
        #expect(FNV1a.hash("a") == 0xaf63_dc4c_8601_ec8c)
    }

    @Test func differentSeedsVary() {
        let styles = (0..<200).map { CardStyle.make(seed: UInt64($0) &* 0x9E37_79B9) }
        #expect(Set(styles.map(\.family)).count == CardFamily.free.count)
        #expect(Set(styles.map { Int($0.palette[1].h) }).count > 100)
    }

    @Test func freeTierNeverRollsAProFamily() {
        for seed in 0..<500 {
            #expect(CardStyle.make(seed: UInt64(seed), allowed: Entitlements.families(pro: false)).family.isFree)
        }
        let pro = Set((0..<500).map { CardStyle.make(seed: UInt64($0), allowed: Entitlements.families(pro: true)).family })
        #expect(pro == Set(CardFamily.allCases))
        #expect(CardFamily.free.count == 3)
    }

    @Test func parametersStayInRange() {
        for seed in 0..<300 {
            let s = CardStyle.make(seed: UInt64(seed) &* 7919)
            #expect(s.palette.count == 4)
            #expect((0.2...1.4).contains(s.warp))
            #expect((1.2...3.2).contains(s.scale))
            #expect(s.palette.allSatisfy { (0..<360).contains($0.h) && (0...1).contains($0.l) })
            // Ground first: the darkest color leads.
            #expect(s.palette[0].l < s.palette[3].l)
        }
    }

    @Test func oklchConvertsToSRGBInGamut() {
        let white = OKLCH(l: 1, c: 0, h: 0).sRGB
        #expect(abs(white.r - 1) < 0.001 && abs(white.g - 1) < 0.001 && abs(white.b - 1) < 0.001)
        let black = OKLCH(l: 0, c: 0, h: 0).sRGB
        #expect(black.r < 0.001 && black.g < 0.001 && black.b < 0.001)
        // A chroma far out of gamut is reduced, not clipped per channel.
        let loud = OKLCH(l: 0.7, c: 0.4, h: 150).sRGB
        #expect([loud.r, loud.g, loud.b].allSatisfy { (0...1).contains($0) })
        #expect(loud.g > loud.r && loud.g > loud.b)
    }
}
