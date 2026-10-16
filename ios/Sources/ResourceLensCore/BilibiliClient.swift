import Foundation

struct BilibiliLink: Equatable, Sendable {
    let bvid: String?
    let aid: Int64?
    let page: Int

    static func isPlatformURL(_ url: URL) -> Bool {
        let host = url.host?.lowercased() ?? ""
        return host == "b23.tv" || host == "bilibili.com" || host.hasSuffix(".bilibili.com")
    }
    static func isMediaURL(_ url: URL) -> Bool {
        let host = url.host?.lowercased() ?? ""
        return ["bilivideo.com", "bilivideo.cn", "akamaized.net"].contains { host == $0 || host.hasSuffix("." + $0) }
    }
    init?(_ url: URL) {
        guard Self.isPlatformURL(url) else { return nil }
        let items = URLComponents(url:url,resolvingAgainstBaseURL:false)?.queryItems ?? []
        let query = Dictionary(items.map { ($0.name,$0.value ?? "") }, uniquingKeysWith: { first,_ in first })
        let candidate = url.path.split(separator:"/").last.map(String.init) ?? ""
        let rawBV = query["bvid"] ?? candidate
        let rawAV = query["aid"] ?? (candidate.lowercased().hasPrefix("av") ? String(candidate.dropFirst(2)) : "")
        bvid = rawBV.range(of:#"^BV[0-9A-Za-z]{10}$"#,options:.regularExpression) != nil ? rawBV : nil
        aid = Int64(rawAV).flatMap { $0 > 0 ? $0 : nil }
        guard bvid != nil || aid != nil else { return nil }
        page = max(1,Int(query["p"] ?? query["page"] ?? "1") ?? 1)
    }
}

struct BilibiliMetadata: Sendable {
    let bvid: String
    let cid: Int64
    let title: String
    let duration: Double
    let origin: URL
}

enum BilibiliClient {
    static let userAgent = "Mozilla/5.0"

    static func resolve(_ input: URL, session: URLSession) async throws -> [MediaSource] {
        var target = input
        if target.host?.lowercased() == "b23.tv" {
            var request = URLRequest(url:target)
            request.setValue(userAgent,forHTTPHeaderField:"User-Agent")
            let (bytes,response) = try await session.bytes(for:request)
            bytes.task.cancel()
            guard let redirected=response.url, BilibiliLink.isPlatformURL(redirected) else { throw SourceError("B站短链接未指向视频，请复制视频的 BV 或 av 分享链接") }
            try SourceRules.validate(redirected)
            target = redirected
        }
        guard let link=BilibiliLink(target) else { throw SourceError("请使用 B站视频的 BV、av 或 b23.tv 分享链接；番剧和直播暂不支持") }
        let viewItems = link.bvid.map { [URLQueryItem(name:"bvid",value:$0)] } ?? [URLQueryItem(name:"aid",value:link.aid.map(String.init))]
        let view = try await json(path:"x/web-interface/view",items:viewItems,session:session)
        let metadata = try parseMetadata(view,link:link)
        let play = try await json(path:"x/player/playurl",items:[
            URLQueryItem(name:"bvid",value:metadata.bvid), URLQueryItem(name:"cid",value:String(metadata.cid)),
            URLQueryItem(name:"qn",value:"80"), URLQueryItem(name:"fnval",value:"1"),
            URLQueryItem(name:"platform",value:"html5"), URLQueryItem(name:"high_quality",value:"1")
        ],session:session)
        return [try parsePlayback(play,metadata:metadata)]
    }

    private static func json(path: String, items: [URLQueryItem], session: URLSession) async throws -> Data {
        var url = URLComponents(string:"https://api.bilibili.com/" + path)!
        url.queryItems = items
        var request = URLRequest(url:url.url!)
        request.setValue(userAgent,forHTTPHeaderField:"User-Agent")
        request.setValue("https://www.bilibili.com/",forHTTPHeaderField:"Referer")
        let (bytes,response) = try await session.bytes(for:request)
        defer { bytes.task.cancel() }
        guard let http=response as? HTTPURLResponse, http.statusCode == 200,
              response.url?.host == "api.bilibili.com", response.url?.scheme == "https" else {
            throw SourceError("B站暂时无法读取，请稍后重试")
        }
        var data=Data();data.reserveCapacity(16384)
        for try await byte in bytes {
            try Task.checkCancellation()
            guard data.count < SourceRules.probeLimit else { throw SourceError("B站返回的数据过大，已停止识别") }
            data.append(byte)
        }
        return data
    }

    private static func payload(_ data: Data) throws -> [String:Any] {
        guard data.count <= SourceRules.probeLimit,
              let json=(try? JSONSerialization.jsonObject(with:data)) as? [String:Any],
              let code=json["code"] as? Int else { throw SourceError("B站未返回有效的视频信息，请稍后重试") }
        guard code == 0 else {
            switch code {
            case -404, 62002: throw SourceError("这条 B站视频不存在或已下架")
            case -101, -10403: throw SourceError("这条 B站视频需要登录或授权，当前独立版无法保存")
            case -352, -412, -403: throw SourceError("B站暂时限制了访问，请稍后重试")
            default: throw SourceError("B站未提供可保存的视频（\(code)），请稍后重试")
            }
        }
        guard let payload=json["data"] as? [String:Any] else { throw SourceError("B站未提供视频数据") }
        return payload
    }

    static func parseMetadata(_ data: Data, link: BilibiliLink) throws -> BilibiliMetadata {
        let payload=try payload(data)
        guard let bvid=payload["bvid"] as? String, bvid.range(of:#"^BV[0-9A-Za-z]{10}$"#,options:.regularExpression) != nil,
              let pages=payload["pages"] as? [[String:Any]], pages.indices.contains(link.page-1),
              let cid=(pages[link.page-1]["cid"] as? NSNumber)?.int64Value, cid > 0,
              let title=payload["title"] as? String, !title.isEmpty else { throw SourceError("B站分P信息无效，请检查视频链接中的页码") }
        let rights=payload["rights"] as? [String:Any] ?? [:]
        if ["pay","ugc_pay"].contains(where:{(rights[$0] as? NSNumber)?.boolValue == true}) || (payload["is_upower_exclusive"] as? NSNumber)?.boolValue == true {
            throw SourceError("这条 B站视频需要购买或授权，当前独立版无法保存")
        }
        let page=pages[link.page-1]
        let part=pages.count > 1 ? " · P\(link.page) " + (page["part"] as? String ?? "") : ""
        var origin=URLComponents(string:"https://www.bilibili.com/video/"+bvid)!
        if link.page > 1 { origin.queryItems=[URLQueryItem(name:"p",value:String(link.page))] }
        return BilibiliMetadata(bvid:bvid,cid:cid,title:title+part,duration:(page["duration"] as? NSNumber)?.doubleValue ?? 0,origin:origin.url!)
    }

    static func parsePlayback(_ data: Data, metadata: BilibiliMetadata) throws -> MediaSource {
        let payload=try payload(data)
        guard (payload["is_preview"] as? NSNumber)?.boolValue != true,
              (payload["drm_tech_type"] as? NSNumber)?.intValue ?? 0 == 0 else { throw SourceError("B站只提供了试看或受保护的播放内容，未创建下载任务") }
        guard let files=payload["durl"] as? [[String:Any]], files.count == 1,
              let value=files[0]["url"] as? String, var url=URL(string:value), BilibiliLink.isMediaURL(url),
              url.pathExtension.lowercased() == "mp4" else { throw SourceError("这条 B站视频暂未提供完整 MP4；DASH 和多段合并尚不支持") }
        if url.scheme == "http", var components=URLComponents(url:url,resolvingAgainstBaseURL:false) { components.scheme="https"; url=components.url ?? url }
        try SourceRules.validate(url)
        let duration=(payload["timelength"] as? NSNumber)?.doubleValue ?? (files[0]["length"] as? NSNumber)?.doubleValue ?? 0
        if metadata.duration > 0, duration/1000 + max(4,metadata.duration*0.02) < metadata.duration { throw SourceError("B站返回的是不完整的试看视频，未创建下载任务") }
        let qn=(payload["quality"] as? NSNumber)?.intValue ?? 0
        let quality=[16:"360p",32:"480p",64:"720p",80:"1080p",112:"1080p+",116:"1080p60" ][qn] ?? "来源画质"
        let size=(files[0]["size"] as? NSNumber)?.int64Value ?? 0
        let title=metadata.title.replacingOccurrences(of:"/",with:"／").replacingOccurrences(of:"\\",with:"＼")
        return MediaSource(url:url,title:SourceRules.fileName(title+" · "+quality+".mp4"),kind:.video,size:size,originURL:metadata.origin,quality:quality)
    }
}
