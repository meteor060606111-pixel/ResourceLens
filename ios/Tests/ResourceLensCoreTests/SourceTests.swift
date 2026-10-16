import XCTest
@testable import ResourceLensCore

final class SourceTests: XCTestCase {
    func testShareTextAndSchemes() throws {
        XCTAssertEqual(try SourceRules.inputURL("喜欢这个视频 https://example.com/movie.mp4 分享给你").host, "example.com")
        XCTAssertThrowsError(try SourceRules.validate(URL(string: "file:///etc/passwd")!))
        XCTAssertThrowsError(try SourceRules.validate(URL(string: "http://example.com/a.mp4")!))
        XCTAssertThrowsError(try SourceRules.validate(URL(string: "https://user:pass@example.com/a.mp4")!))
        XCTAssertNoThrow(try SourceRules.validate(URL(string: "http://127.0.0.1/a.mp4")!, allowLocalHTTP: true))
    }
    func testPageMetadataAndAudioExclusion() throws {
        let html = #"<video src="/media/a.mp4?x=1&amp;y=2"></video><source src='/media/a.mp4?x=1&amp;y=2'><meta content="https://cdn.example/b.m3u8" property="og:video"><meta property="og:image" content="/cover.jpg"><source src="https://example.com/a.mp3"><video src="javascript:bad()">"#
        let sources = MediaResolver.sources(in: html, base: URL(string: "https://example.com/page")!)
        XCTAssertEqual(sources.map(\.kind), [.video, .hls, .image])
        XCTAssertEqual(sources.first?.url.absoluteString, "https://example.com/media/a.mp4?x=1&y=2")
        XCTAssertThrowsError(try SourceRules.kind(url: URL(string: "https://example.com/song.mp3")!))
        XCTAssertNil(try SourceRules.kind(url: URL(string: "https://example.com/login.mp4")!, mime: "text/html"))
    }
    func testMetadataIsBounded() {
        let html = (0..<100).map { "<video src='https://example.com/\($0).mp4'>" }.joined()
        XCTAssertEqual(MediaResolver.sources(in: html, base: URL(string: "https://example.com")!).count, 20)
        XCTAssertTrue(MediaResolver.sources(in: String(repeating:"x", count:SourceRules.probeLimit+1),base:URL(string:"https://example.com")!).isEmpty)
    }
    func testOwnedPathsAndFilenames() {
        let root = URL(fileURLWithPath: "/tmp/root")
        XCTAssertNil(SourceRules.ownedURL(relativePath: "../private", root: root))
        XCTAssertNil(SourceRules.ownedURL(relativePath: "/etc/passwd", root: root))
        XCTAssertNotNil(SourceRules.ownedURL(relativePath: "Downloads/id/a.mp4", root: root))
        XCTAssertEqual(SourceRules.fileName("../../movie.mp4"), "movie.mp4")
        XCTAssertEqual(SourceRules.fileName(".."), "下载文件")
    }
    func testFalseVideoAndHTTPFailuresAreNotSaved() throws {
        let file = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: file) }
        try Data("<!DOCTYPE html><html>sign in</html>".utf8).write(to:file)
        XCTAssertThrowsError(try SourceRules.validateFile(file, status:200, mime:"video/mp4"))
        try Data([0,1,2,3]).write(to:file)
        XCTAssertThrowsError(try SourceRules.validateFile(file,status:403,mime:"application/octet-stream"))
        XCTAssertEqual(try SourceRules.validateFile(file,status:200,mime:"application/octet-stream"),4)
        XCTAssertThrowsError(try SourceRules.validateFile(file,status:206,mime:"application/octet-stream",declaredSize:8))
        XCTAssertThrowsError(try SourceRules.validateFile(file,status:200,mime:"video/mp4",expectedKind:.video))
        try Data([0,0,0,24]+Array("ftypisom".utf8)+Array(repeating:0,count:12)).write(to:file)
        XCTAssertNoThrow(try SourceRules.validateFile(file,status:200,mime:"video/mp4",expectedKind:.video))
    }

    func testSystemPathAliasesRemainRelative() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let real = root.appendingPathComponent("real", isDirectory:true)
        let alias = root.appendingPathComponent("alias", isDirectory:true)
        try FileManager.default.createDirectory(at:real,withIntermediateDirectories:true)
        defer {try? FileManager.default.removeItem(at:root)}
        try FileManager.default.createSymbolicLink(at:alias,withDestinationURL:real)
        let file = real.appendingPathComponent("offline.movpkg")
        try Data([1]).write(to:file)
        XCTAssertEqual(SourceRules.relativePath(of:file,root:alias),"offline.movpkg")
        XCTAssertEqual(SourceRules.ownedURL(relativePath:"offline.movpkg",root:alias),file.resolvingSymlinksInPath())
        XCTAssertNil(SourceRules.relativePath(of:root.appendingPathComponent("outside"),root:alias))
    }
}
