// swift-tools-version: 5.9
import PackageDescription

let package = Package(name: "ResourceLensCore", platforms: [.iOS(.v17), .macOS(.v13)], products: [.library(name: "ResourceLensCore", targets: ["ResourceLensCore"])], targets: [.target(name: "ResourceLensCore"), .testTarget(name: "ResourceLensCoreTests", dependencies: ["ResourceLensCore"])])
