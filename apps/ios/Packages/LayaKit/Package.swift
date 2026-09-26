// swift-tools-version: 6.2
// LayaKit runs Laya typed-decision checkpoints (github.com/mizorewww/laya-coreml)
// on device through Core ML: tokenizer, prompt, decoding and download, with no
// Python and no dependency beyond the SDK.
import PackageDescription

let package = Package(
    name: "LayaKit",
    platforms: [.iOS("26.0"), .macOS("26.0")],
    products: [
        .library(name: "LayaKit", targets: ["LayaKit"]),
    ],
    targets: [
        .target(name: "LayaKit"),
        .testTarget(
            name: "LayaKitTests",
            dependencies: ["LayaKit"],
            resources: [.copy("Fixtures")]
        ),
    ]
)
