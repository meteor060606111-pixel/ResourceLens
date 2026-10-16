import Foundation

public enum MediaKind: String, Codable, Sendable {
    case video, image, file, hls
    public var label: String { switch self { case .video: return "视频"; case .image: return "图片"; case .file: return "文件"; case .hls: return "HLS 离线视频" } }
    public var symbol: String { switch self { case .video, .hls: return "video"; case .image: return "photo"; case .file: return "doc" } }
}

public struct MediaSource: Identifiable, Codable, Hashable, Sendable {
    public var id: String { url.absoluteString }
    public let url: URL
    public let title: String
    public let kind: MediaKind
    public let size: Int64
    public init(url: URL, title: String, kind: MediaKind, size: Int64 = 0) {
        self.url = url; self.title = title; self.kind = kind; self.size = size
    }
}

public struct SourceError: LocalizedError, Sendable {
    public let message: String
    public var errorDescription: String? { message }
    public init(_ message: String) { self.message = message }
}

public enum SourceRules {
    public static let probeLimit = 1024 * 1024
    public static let recordLimit = 500

    public static func inputURL(_ text: String) throws -> URL {
        guard text.utf8.count <= 16384 else { throw SourceError("分享文本过长，请只粘贴链接") }
        let detector = try NSDataDetector(types: NSTextCheckingResult.CheckingType.link.rawValue)
        let range = NSRange(text.startIndex..., in: text)
        guard let url = detector.matches(in: text, range: range).compactMap(\.url).first,
              ["https", "http"].contains(url.scheme?.lowercased()), url.host != nil,
              url.user == nil, url.password == nil else { throw SourceError("请粘贴有效的视频、图片、文件或网页链接") }
        return url
    }

    public static func validate(_ url: URL, allowLocalHTTP: Bool = false) throws {
        guard url.user == nil, url.password == nil, let host = url.host, !host.isEmpty,
              url.absoluteString.utf8.count <= 8192 else { throw SourceError("链接格式无效") }
        let local = ["localhost", "127.0.0.1", "::1"].contains(host)
        guard url.scheme?.lowercased() == "https" || allowLocalHTTP && local && url.scheme == "http" else {
            throw SourceError("独立版使用 HTTPS 下载，请提供安全的媒体链接")
        }
    }

    public static func kind(url: URL, mime: String = "") throws -> MediaKind? {
        let type = mime.lowercased().split(separator: ";").first.map(String.init) ?? ""
        let ext = url.pathExtension.lowercased()
        if ext == "mpd" || type.contains("dash+xml") {throw SourceError("首版暂不合并 DASH 分片，请使用完整视频或 HLS 点播链接")}
        if type.hasPrefix("audio/") || ["mp3", "m4a", "aac", "flac", "wav", "ogg"].contains(ext) {
            throw SourceError("手机版专注视频、图片和文件，不包含音乐下载")
        }
        if type.contains("mpegurl") || ext == "m3u8" { return .hls }
        if type == "text/html" || type == "application/xhtml+xml" { return nil }
        if type.hasPrefix("video/") || ["mp4", "mov", "m4v", "webm", "mkv"].contains(ext) { return .video }
        if type.hasPrefix("image/") || ["jpg", "jpeg", "png", "gif", "webp", "heic", "avif"].contains(ext) { return .image }
        if !ext.isEmpty && !["html", "htm", "php", "asp", "aspx"].contains(ext) { return .file }
        if !type.isEmpty && !type.hasPrefix("text/") { return .file }
        return nil
    }

    public static func fileName(_ proposed: String, fallback: String = "下载文件") -> String {
        let leaf = proposed.replacingOccurrences(of: "\\", with: "/").split(separator: "/").last.map(String.init) ?? ""
        let safe = leaf.components(separatedBy: .controlCharacters.union(CharacterSet(charactersIn: ":?*<>|\""))).joined(separator: "_").trimmingCharacters(in: .whitespacesAndNewlines)
        let result = String(safe.prefix(160))
        return result.isEmpty || result == "." || result == ".." ? fallback : result
    }

    public static func validateFile(_ file: URL, status: Int, mime: String, expectedKind: MediaKind? = nil, declaredSize: Int64? = nil) throws -> Int64 {
        guard (200...299).contains(status) else { throw SourceError("来源返回 HTTP \(status)，文件未保存") }
        let size = (try FileManager.default.attributesOfItem(atPath: file.path)[.size] as? NSNumber)?.int64Value ?? 0
        guard size > 0 else { throw SourceError("来源返回了空文件，请更换链接") }
        if let declaredSize, declaredSize >= 0, size != declaredSize {throw SourceError("文件长度与来源声明不一致，请继续或重新下载")}
        let handle = try FileHandle(forReadingFrom: file)
        defer { try? handle.close() }
        let header = try handle.read(upToCount: 512) ?? Data()
        let prefix = String(decoding: header, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !mime.lowercased().contains("text/html"), !prefix.hasPrefix("<!doctype html"), !prefix.hasPrefix("<html") else { throw SourceError("来源返回了网页，未将它保存为视频") }
        if expectedKind == .video || mime.lowercased().hasPrefix("video/") {
            let iso = header.count >= 12 && String(decoding: header[4..<8], as: UTF8.self) == "ftyp"
            let ebml = header.starts(with: [0x1a,0x45,0xdf,0xa3])
            let avi = header.count >= 12 && String(decoding:header.prefix(4),as:UTF8.self)=="RIFF" && String(decoding:header[8..<12],as:UTF8.self)=="AVI "
            guard iso || ebml || avi else {throw SourceError("下载内容不是可识别的视频文件，请更换链接")}
        }
        return Int64(size)
    }

    public static func ownedURL(relativePath: String, root: URL) -> URL? {
        guard !relativePath.isEmpty, !relativePath.hasPrefix("/"), !relativePath.split(separator: "/").contains("..") else { return nil }
        let file = root.appendingPathComponent(relativePath).resolvingSymlinksInPath().standardizedFileURL
        let base = root.resolvingSymlinksInPath().standardizedFileURL.path + "/"
        return file.path.hasPrefix(base) ? file : nil
    }
}
