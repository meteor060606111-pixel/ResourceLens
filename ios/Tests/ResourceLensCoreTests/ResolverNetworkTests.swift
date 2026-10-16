import XCTest
@testable import ResourceLensCore

final class ResolverNetworkTests: XCTestCase {
    func testActualProbeAndPageResolution() async throws {
        guard let port = ProcessInfo.processInfo.environment["RESOURCELENS_IOS_FIXTURE_PORT"] else {throw XCTSkip("Start the fixture server to exercise real HTTP")}
        let resolver=MediaResolver(allowLocalHTTP:true)
        let base=URL(string:"http://127.0.0.1:\(port)")!
        let image=try await resolver.resolve(base.appendingPathComponent("sample.png"))
        XCTAssertEqual(image.first?.kind,.image);XCTAssertEqual(image.first?.size,68)
        let file=try await resolver.resolve(base.appendingPathComponent("slow.bin"))
        XCTAssertEqual(file.first?.kind,.file);XCTAssertEqual(file.first?.size,8*1024*1024)
        let page=try await resolver.resolve(base.appendingPathComponent("page"))
        XCTAssertEqual(page.first?.url,base.appendingPathComponent("sample.png"));XCTAssertEqual(page.first?.kind,.image)
        do {_ = try await resolver.resolve(base.appendingPathComponent("not-a-video.mp4"));XCTFail("HTML was accepted as video")}catch {XCTAssertTrue(error is SourceError)}
        do {_ = try await resolver.resolve(base.appendingPathComponent("live.m3u8"));XCTFail("Live stream was accepted")}catch {XCTAssertTrue(error is SourceError)}
    }
}
