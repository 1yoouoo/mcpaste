// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "MCPaste",
    platforms: [.macOS(.v14)],
    products: [
        .library(name: "MCPasteCore", targets: ["MCPasteCore"]),
        .executable(name: "MCPaste", targets: ["MCPasteApp"])
    ],
    targets: [
        .systemLibrary(name: "CSQLite", path: "Sources/CSQLite"),
        .target(name: "MCPasteCore", dependencies: ["CSQLite"]),
        .executableTarget(name: "MCPasteApp", dependencies: ["MCPasteCore"]),
        .testTarget(name: "MCPasteCoreTests", dependencies: ["MCPasteCore"]),
        .testTarget(name: "MCPasteAppTests", dependencies: ["MCPasteApp"])
    ]
)
