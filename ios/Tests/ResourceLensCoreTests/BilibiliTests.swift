import XCTest
@testable import ResourceLensCore

final class BilibiliTests: XCTestCase {
    private let bv="BV1xx411c7mD"
    private func json(_ object: [String:Any]) throws -> Data {try JSONSerialization.data(withJSONObject:object)}
    private func metadata() -> BilibiliMetadata {BilibiliMetadata(bvid:bv,cid:123,title:"测试/视频",duration:60,origin:URL(string:"https://www.bilibili.com/video/"+bv)!)}

    func testFullMobilePlayerAndAVLinks() {
        let full=BilibiliLink(URL(string:"https://www.bilibili.com/video/\(bv)?p=2&share_source=copy")!)
        XCTAssertEqual(full?.bvid,bv);XCTAssertEqual(full?.page,2)
        XCTAssertEqual(BilibiliLink(URL(string:"https://m.bilibili.com/video/\(bv)")!)?.bvid,bv)
        XCTAssertEqual(BilibiliLink(URL(string:"https://player.bilibili.com/player.html?bvid=\(bv)&page=3")!)?.page,3)
        XCTAssertEqual(BilibiliLink(URL(string:"https://www.bilibili.com/video/av12345")!)?.aid,12345)
        XCTAssertTrue(BilibiliLink.isPlatformURL(URL(string:"https://b23.tv/short")!))
        XCTAssertFalse(BilibiliLink.isPlatformURL(URL(string:"https://bilibili.com.example/video/\(bv)")!))
        XCTAssertNil(BilibiliLink(URL(string:"https://www.bilibili.com/video/not-a-bvid")!))
    }

    func testSelectedPartAndPermissionErrors() throws {
        let data=try json(["code":0,"data":["bvid":bv,"title":"标题","pages":[["cid":1,"part":"第一部分","duration":10],["cid":2,"part":"第二部分","duration":20]]]])
        let link=BilibiliLink(URL(string:"https://www.bilibili.com/video/\(bv)?p=2")!)!
        let parsed=try BilibiliClient.parseMetadata(data,link:link)
        XCTAssertEqual(parsed.cid,2);XCTAssertEqual(parsed.duration,20);XCTAssertTrue(parsed.title.contains("P2"))
        XCTAssertEqual(parsed.origin.query,"p=2")
        XCTAssertThrowsError(try BilibiliClient.parseMetadata(try json(["code":-101]),link:link))
        let paid=try json(["code":0,"data":["bvid":bv,"title":"付费","rights":["pay":1],"pages":[["cid":1],["cid":2]]]])
        XCTAssertThrowsError(try BilibiliClient.parseMetadata(paid,link:link))
    }

    func testCompleteMP4HasCorrectQualityAndDownloadHeaders() throws {
        let data=try json(["code":0,"data":["quality":64,"timelength":60000,"durl":[["url":"http://cdn.bilivideo.com/video.mp4","size":123456,"length":60000]]]])
        let source=try BilibiliClient.parsePlayback(data,metadata:metadata())
        XCTAssertEqual(source.kind,.video);XCTAssertEqual(source.quality,"720p");XCTAssertEqual(source.size,123456)
        XCTAssertEqual(source.url.scheme,"https");XCTAssertTrue(source.title.hasSuffix("720p.mp4"))
        XCTAssertTrue(source.title.hasPrefix("测试／视频"))
        XCTAssertEqual(source.downloadRequest.value(forHTTPHeaderField:"Referer"),metadata().origin.absoluteString)
        XCTAssertNotNil(source.downloadRequest.value(forHTTPHeaderField:"User-Agent"))
        let restored=try JSONDecoder().decode(MediaSource.self,from:JSONEncoder().encode(source))
        XCTAssertEqual(restored.downloadRequest.value(forHTTPHeaderField:"Referer"),source.downloadRequest.value(forHTTPHeaderField:"Referer"))
    }

    func testPreviewDashAndMultiPartCannotBeReportedAsFullVideo() throws {
        let file:[String:Any]=["url":"https://cdn.bilivideo.com/v.mp4","length":1000]
        for payload:[String:Any] in [["is_preview":true,"durl":[file]],["timelength":1000,"durl":[file]],["durl":[file,file]],["dash":["video":[]]],["drm_tech_type":2,"durl":[file]]] {
            XCTAssertThrowsError(try BilibiliClient.parsePlayback(json(["code":0,"data":payload]),metadata:metadata()))
        }
    }

    func testLegacySourceStillLoadsAndCoverIsExplicit() throws {
        let old=try json(["url":"https://player.bilibili.com/player.html?bvid="+bv,"title":"player.html","kind":"video","size":7000])
        let source=try JSONDecoder().decode(MediaSource.self,from:old)
        XCTAssertNil(source.originURL);XCTAssertNil(source.quality)
        XCTAssertNotNil(BilibiliLink(source.url))
        let page=#"<meta property="og:video" content="https://player.bilibili.com/player.html?bvid=BV1xx411c7mD"><meta property="og:image" content="https://cdn.example/cover.jpg">"#
        let result=MediaResolver.sources(in:page,base:URL(string:"https://example.com/watch")!)
        XCTAssertEqual(result.count,1);XCTAssertEqual(result[0].kind,.image)
        XCTAssertTrue(result[0].title.hasPrefix("网页封面"))
    }

    func testUnicodeTitleFitsTheFilesystemAndKeepsExtension() {
        let name=SourceRules.fileName(String(repeating:"长视频名字🎞️",count:80)+".mp4")
        XCTAssertLessThanOrEqual(name.utf8.count,220);XCTAssertTrue(name.hasSuffix(".mp4"))
    }

    func testReportedPublicVideosResolveToRealMP4() async throws {
        guard let input=ProcessInfo.processInfo.environment["RESOURCELENS_BILIBILI_SMOKE"], !input.isEmpty else {throw XCTSkip("Optional live Bilibili compatibility check; supply comma-separated public BV IDs")}
        for bvid in input.split(separator:",").prefix(2).map(String.init) {
            let sources=try await MediaResolver().resolve(URL(string:"https://player.bilibili.com/player.html?bvid="+bvid)!)
            XCTAssertEqual(sources.count,1)
            let source=try XCTUnwrap(sources.first)
            XCTAssertEqual(source.kind,.video);XCTAssertEqual(source.quality,"720p")
            XCTAssertTrue(source.size>1024*1024);XCTAssertFalse(source.title.contains("player.html"))
            var request=source.downloadRequest;request.setValue("bytes=0-31",forHTTPHeaderField:"Range")
            let (bytes,response)=try await URLSession.shared.bytes(for:request)
            defer {bytes.task.cancel()}
            XCTAssertEqual((response as? HTTPURLResponse)?.statusCode,206)
            var header=Data()
            for try await byte in bytes {header.append(byte);if header.count == 32 {break}}
            XCTAssertEqual(String(decoding:header.dropFirst(4).prefix(4),as:UTF8.self),"ftyp")
        }
    }
}
