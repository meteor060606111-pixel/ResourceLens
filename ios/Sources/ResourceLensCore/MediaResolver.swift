import Foundation

public final class MediaResolver: @unchecked Sendable {
    private let session: URLSession
    private let allowLocalHTTP: Bool
    public init(allowLocalHTTP: Bool = false) {
        self.allowLocalHTTP = allowLocalHTTP
        let config = URLSessionConfiguration.ephemeral
        config.urlCache = nil; config.httpCookieStorage = nil
        config.timeoutIntervalForRequest = 25
        config.httpMaximumConnectionsPerHost = 2
        session = URLSession(configuration: config)
    }
    deinit { session.invalidateAndCancel() }

    public func resolve(_ url: URL) async throws -> [MediaSource] {
        if BilibiliLink.isPlatformURL(url) {
            var secure=url
            if url.scheme == "http", var parts=URLComponents(url:url,resolvingAgainstBaseURL:false) { parts.scheme="https";secure=parts.url ?? url }
            try SourceRules.validate(secure)
            return try await BilibiliClient.resolve(secure,session:session)
        }
        try SourceRules.validate(url, allowLocalHTTP: allowLocalHTTP)
        _ = try SourceRules.kind(url: url)
        var request = URLRequest(url: url)
        request.setValue("bytes=0-\(SourceRules.probeLimit-1)", forHTTPHeaderField: "Range")
        request.setValue("ResourceLens-iOS/0.1", forHTTPHeaderField: "User-Agent")
        let (bytes, response) = try await session.bytes(for: request)
        defer { bytes.task.cancel() }
        guard let http = response as? HTTPURLResponse, (200...299).contains(http.statusCode) else { throw SourceError("无法读取链接，请检查网络或更换来源") }
        let finalURL = response.url ?? url
        try SourceRules.validate(finalURL, allowLocalHTTP: allowLocalHTTP)
        let kind = try SourceRules.kind(url: finalURL, mime: response.mimeType ?? "")
        let name = SourceRules.fileName(response.suggestedFilename ?? finalURL.lastPathComponent)
        let size = Int64(http.value(forHTTPHeaderField: "Content-Range")?.split(separator: "/").last ?? "") ?? max(0, response.expectedContentLength)
        if let kind, kind != .hls { return [MediaSource(url: finalURL, title: name, kind: kind, size: size)] }
        var data = Data(); data.reserveCapacity(32768)
        for try await byte in bytes {
            try Task.checkCancellation()
            guard data.count < SourceRules.probeLimit else { throw SourceError("网页或播放清单过大，请使用直接媒体链接") }
            data.append(byte)
        }
        let text = String(decoding: data, as: UTF8.self)
        if kind == .hls || text.trimmingCharacters(in: .whitespacesAndNewlines).hasPrefix("#EXTM3U") {
            guard text.contains("#EXT-X-ENDLIST") || text.contains("#EXT-X-STREAM-INF") else { throw SourceError("当前是直播清单，请提供已结束的视频或文件链接") }
            return [MediaSource(url: finalURL, title: name, kind: .hls)]
        }
        let sources = Self.sources(in: text, base: finalURL, allowLocalHTTP: allowLocalHTTP)
        guard !sources.isEmpty else { throw SourceError("这个网页没有提供可直接保存的视频或图片。请粘贴媒体直链；需要登录或专用解析的平台暂不支持") }
        return sources
    }

    public static func sources(in html: String, base: URL, allowLocalHTTP: Bool = false) -> [MediaSource] {
        guard html.utf8.count <= SourceRules.probeLimit else { return [] }
        var values: [(String, MediaKind)] = []
        let tags = matches(#"<(?:video|source|meta)\b[^>]*>"#, html)
        for tag in tags {
            let attributes = matches(#"([a-zA-Z_:]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))"#, tag)
            var attrs: [String:String] = [:]
            for attribute in attributes {
                let pieces = attribute.split(separator: "=", maxSplits: 1)
                guard pieces.count == 2 else { continue }
                attrs[pieces[0].trimmingCharacters(in: .whitespacesAndNewlines).lowercased()] = String(pieces[1]).trimmingCharacters(in: .whitespacesAndNewlines).trimmingCharacters(in: CharacterSet(charactersIn: "\"'"))
            }
            if tag.lowercased().hasPrefix("<meta") {
                let property = (attrs["property"] ?? attrs["name"] ?? "").lowercased()
                if ["og:video", "og:video:url", "og:video:secure_url", "twitter:player:stream"].contains(property), let content = attrs["content"] { values.append((content, .video)) }
                if property == "og:image", let content = attrs["content"] { values.append((content, .image)) }
            } else if let src = attrs["src"] { values.append((src, .video)) }
        }
        // JSON-LD contentUrl is metadata, not executable page JavaScript.
        for entry in matches(#""contentUrl"\s*:\s*"([^"]+)""#, html) {
            if let separator = entry.firstIndex(of: ":") { values.append((String(entry[entry.index(after: separator)...]).trimmingCharacters(in: .whitespacesAndNewlines).trimmingCharacters(in: CharacterSet(charactersIn: "\"")), .video)) }
        }
        var seen = Set<String>(), result: [MediaSource] = []
        for (value, hint) in values {
            let decoded = value.replacingOccurrences(of: "&amp;", with: "&").replacingOccurrences(of: "&#38;", with: "&").replacingOccurrences(of: "\\/", with: "/").replacingOccurrences(of: "\\u0026", with: "&")
            guard let url = URL(string: decoded, relativeTo: base)?.absoluteURL,
                  (try? SourceRules.validate(url, allowLocalHTTP: allowLocalHTTP)) != nil,
                  seen.insert(url.absoluteString).inserted else { continue }
            let type: MediaKind?
            do { type = try SourceRules.kind(url: url) } catch { continue }
            if hint == .video, ["html","htm"].contains(url.pathExtension.lowercased()) { continue }
            let kind = type == .file ? hint : type ?? hint
            let title=(hint == .image ? "网页封面 · " : "")+SourceRules.fileName(url.lastPathComponent)
            result.append(MediaSource(url: url, title: title, kind: kind))
            if result.count == 20 { break }
        }
        return result
    }

    private static func matches(_ pattern: String, _ text: String) -> [String] {
        guard let regex = try? NSRegularExpression(pattern: pattern, options: [.caseInsensitive]) else { return [] }
        return regex.matches(in: text, range: NSRange(text.startIndex..., in: text)).compactMap { Range($0.range, in: text).map { String(text[$0]) } }
    }
}
