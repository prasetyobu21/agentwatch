// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "AgentWatch",
    platforms: [
        .macOS(.v13)
    ],
    products: [
        .executable(
            name: "AgentWatch",
            targets: ["AgentWatch"]
        )
    ],
    dependencies: [],
    targets: [
        .executableTarget(
            name: "AgentWatch",
            dependencies: [],
            path: "Sources/AgentWatch"
        )
    ]
)
