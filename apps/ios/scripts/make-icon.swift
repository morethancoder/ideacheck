// Draws the 1024×1024 app icon: a glowing spark orb on ink.
//   swift apps/ios/scripts/make-icon.swift apps/ios/Sparkjudge/Resources/Assets.xcassets/AppIcon.appiconset/AppIcon.png
import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

let out = CommandLine.arguments.dropFirst().first ?? "AppIcon.png"
let size = 1024
let space = CGColorSpace(name: CGColorSpace.sRGB)!
let ctx = CGContext(data: nil, width: size, height: size, bitsPerComponent: 8, bytesPerRow: 0,
                    space: space, bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)!
let s = CGFloat(size)
func color(_ r: CGFloat, _ g: CGFloat, _ b: CGFloat, _ a: CGFloat = 1) -> CGColor { CGColor(colorSpace: space, components: [r, g, b, a])! }

// Ground.
ctx.setFillColor(color(0.043, 0.039, 0.059))
ctx.fill(CGRect(x: 0, y: 0, width: s, height: s))

let center = CGPoint(x: s / 2, y: s / 2)
// Glow.
let glow = CGGradient(colorsSpace: space, colors: [color(0.98, 0.48, 0.18, 0.55), color(0.49, 0.36, 1.0, 0.18), color(0.043, 0.039, 0.059, 0)] as CFArray, locations: [0, 0.55, 1])!
ctx.drawRadialGradient(glow, startCenter: center, startRadius: 0, endCenter: center, endRadius: s * 0.5, options: [])
// Orb body.
let body = CGGradient(colorsSpace: space, colors: [color(1.0, 0.78, 0.45), color(0.98, 0.48, 0.18), color(0.55, 0.12, 0.45), color(0.12, 0.08, 0.35)] as CFArray, locations: [0, 0.35, 0.75, 1])!
ctx.saveGState()
ctx.addEllipse(in: CGRect(x: center.x - s * 0.27, y: center.y - s * 0.27, width: s * 0.54, height: s * 0.54))
ctx.clip()
ctx.drawRadialGradient(body, startCenter: CGPoint(x: center.x - s * 0.08, y: center.y + s * 0.1), startRadius: 0,
                       endCenter: center, endRadius: s * 0.3, options: [.drawsAfterEndLocation])
ctx.restoreGState()
// Spark: a four-point star.
ctx.setFillColor(color(1, 1, 1, 0.92))
let r1 = s * 0.12, r2 = s * 0.022
let path = CGMutablePath()
for i in 0..<8 {
    let a = CGFloat(i) * .pi / 4 + .pi / 2
    let r = i % 2 == 0 ? r1 : r2
    let p = CGPoint(x: center.x + cos(a) * r, y: center.y + sin(a) * r)
    i == 0 ? path.move(to: p) : path.addLine(to: p)
}
path.closeSubpath()
ctx.addPath(path)
ctx.fillPath()

let image = ctx.makeImage()!
let dest = CGImageDestinationCreateWithURL(URL(fileURLWithPath: out) as CFURL, UTType.png.identifier as CFString, 1, nil)!
CGImageDestinationAddImage(dest, image, nil)
CGImageDestinationFinalize(dest)
print("wrote \(out)")
