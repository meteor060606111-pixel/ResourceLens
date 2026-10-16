import SwiftUI
import AVFoundation
import UniformTypeIdentifiers

enum DownloadStatus: String, Codable {
    case queued, resolving, downloading, pausing, paused, completed, failed
    var label: String { switch self { case .queued: return "排队中"; case .resolving: return "正在重新识别视频"; case .downloading: return "正在下载"; case .pausing: return "正在保留进度"; case .paused: return "已暂停"; case .completed: return "已保存到 iPhone"; case .failed: return "未完成" } }
}

struct DownloadRecord: Identifiable, Codable {
    var id = UUID()
    var source: MediaSource
    var status = DownloadStatus.queued
    var created = Date()
    var received: Int64 = 0
    var expected: Int64 = 0
    var fraction: Double = 0
    var localPath: String?
    var taskID: Int?
    var note = ""
}

enum TransferEvent {
    case progress(UUID, Int, Int64, Int64, Double?)
    case file(UUID, Int, URL, Int64)
    case hlsLocation(UUID, Int, URL)
    case ended(UUID, Int, Error?, Data?)
    case eventsFinished(String)
}

// The temporary download must be moved before URLSession's delegate returns.
// Only the small event payload crosses to the main actor; file bytes never do.
final class TransferDelegate: NSObject, URLSessionDownloadDelegate, AVAssetDownloadDelegate, @unchecked Sendable {
    weak var owner: DownloadStore?
    let documents: URL
    init(documents: URL) { self.documents = documents }
    private func emit(_ event: TransferEvent) { DispatchQueue.main.async { [weak owner] in owner?.receive(event) } }
    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didWriteData bytesWritten: Int64, totalBytesWritten: Int64, totalBytesExpectedToWrite: Int64) {
        guard let id = downloadTask.taskDescription.flatMap(UUID.init(uuidString:)) else { return }
        emit(.progress(id, downloadTask.taskIdentifier, totalBytesWritten, totalBytesExpectedToWrite, nil))
    }
    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didFinishDownloadingTo location: URL) {
        guard let id = downloadTask.taskDescription.flatMap(UUID.init(uuidString:)) else { return }
        do {
            let response = downloadTask.response as? HTTPURLResponse
            let sourceKind = try? SourceRules.kind(url: downloadTask.originalRequest?.url ?? URL(string:"https://invalid.example")!)
            let rangeTotal = response?.value(forHTTPHeaderField:"Content-Range")?.split(separator:"/").last.flatMap {Int64($0)}
            let length = response?.value(forHTTPHeaderField:"Content-Encoding") == nil && response?.statusCode == 200 ? response?.value(forHTTPHeaderField:"Content-Length").flatMap(Int64.init) : nil
            let count = try SourceRules.validateFile(location, status: response?.statusCode ?? 0, mime: response?.mimeType ?? "", expectedKind: sourceKind, declaredSize:rangeTotal ?? length)
            _ = try SourceRules.kind(url: response?.url ?? URL(string:"https://invalid.example")!, mime: response?.mimeType ?? "")
            let dir = documents.appendingPathComponent("Downloads/" + id.uuidString, isDirectory: true)
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            var name = SourceRules.fileName(response?.suggestedFilename ?? downloadTask.originalRequest?.url?.lastPathComponent ?? "下载文件")
            if URL(fileURLWithPath: name).pathExtension.isEmpty, let mime = response?.mimeType, let ext = UTType(mimeType: mime)?.preferredFilenameExtension { name += "." + ext }
            let destination = dir.appendingPathComponent(name)
            // A completed task owns its UUID directory; never overwrite another task.
            if FileManager.default.fileExists(atPath: destination.path) { throw SourceError("目标文件已存在，请先在文件页检查") }
            try FileManager.default.moveItem(at: location, to: destination)
            emit(.file(id, downloadTask.taskIdentifier, destination, count))
        } catch { emit(.ended(id, downloadTask.taskIdentifier, error, nil)) }
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        guard let id = task.taskDescription.flatMap(UUID.init(uuidString:)) else { return }
        emit(.ended(id, task.taskIdentifier, error, (error as NSError?)?.userInfo[NSURLSessionDownloadTaskResumeData] as? Data))
    }
    func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) { emit(.eventsFinished(session.configuration.identifier ?? "")) }
    func urlSession(_ session: URLSession, assetDownloadTask: AVAssetDownloadTask, willDownloadTo location: URL) {
        if let id = assetDownloadTask.taskDescription.flatMap(UUID.init(uuidString:)) { emit(.hlsLocation(id, assetDownloadTask.taskIdentifier, location)) }
    }
    func urlSession(_ session: URLSession, assetDownloadTask: AVAssetDownloadTask, didFinishDownloadingTo location: URL) {
        if let id = assetDownloadTask.taskDescription.flatMap(UUID.init(uuidString:)) { emit(.hlsLocation(id, assetDownloadTask.taskIdentifier, location)) }
    }
    func urlSession(_ session: URLSession, assetDownloadTask: AVAssetDownloadTask, didLoad timeRange: CMTimeRange, totalTimeRangesLoaded loadedTimeRanges: [NSValue], timeRangeExpectedToLoad: CMTimeRange) {
        guard let id = assetDownloadTask.taskDescription.flatMap(UUID.init(uuidString:)) else { return }
        let duration = timeRangeExpectedToLoad.duration.seconds
        let downloaded = loadedTimeRanges.reduce(0) { $0 + $1.timeRangeValue.duration.seconds }
        if duration.isFinite && duration > 0 { emit(.progress(id, assetDownloadTask.taskIdentifier, 0, 0, min(0.99, max(0, downloaded/duration)))) }
    }
}

@MainActor final class DownloadStore: ObservableObject {
    static let shared = DownloadStore()
    @Published private(set) var records: [DownloadRecord] = []
    @Published var message: String?
    let testing: Bool
    let documents: URL
    private let home = URL(fileURLWithPath: NSHomeDirectory(), isDirectory: true)
    private let support: URL
    private var delegate: TransferDelegate!
    private var session: URLSession!
    private var assets: AVAssetDownloadURLSession?
    private var tasks: [UUID: URLSessionTask] = [:]
    private var resolving: [UUID: Task<Void,Never>] = [:]
    private var handlers: [String: () -> Void] = [:]
    private var lastProgress: [UUID: Date] = [:]
    private var lastSave = Date.distantPast

    private init() {
        #if targetEnvironment(macCatalyst) && DEBUG
        testing = true
        #elseif DEBUG
        testing = ProcessInfo.processInfo.arguments.contains("--uitesting")
        #else
        testing = false
        #endif
        let fm = FileManager.default
        documents = fm.urls(for: .documentDirectory, in: .userDomainMask)[0].appendingPathComponent(testing ? "UITesting" : "", isDirectory: true)
        support = fm.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0].appendingPathComponent(testing ? "ResourceLens-UITesting" : "ResourceLens", isDirectory: true)
        if testing { try? fm.removeItem(at: documents); try? fm.removeItem(at: support) }
        do {
            try fm.createDirectory(at: documents, withIntermediateDirectories: true)
            try fm.createDirectory(at: support, withIntermediateDirectories: true)
            let state = support.appendingPathComponent("downloads.json")
            if let count = try? state.resourceValues(forKeys: [.fileSizeKey]).fileSize, count <= 2*1024*1024,
               let data = try? Data(contentsOf: state), let restored = try? JSONDecoder().decode([DownloadRecord].self, from: data) {
                records = Array(restored.prefix(SourceRules.recordLimit))
            }
        } catch { message = "无法准备保存位置：" + error.localizedDescription }
        delegate = TransferDelegate(documents: documents); delegate.owner = self
        let identifier = Bundle.main.bundleIdentifier ?? "com.meteor.ResourceLens.iOS"
        let config = testing ? URLSessionConfiguration.ephemeral : URLSessionConfiguration.background(withIdentifier: identifier + ".files")
        config.urlCache = nil; config.httpMaximumConnectionsPerHost = 2
        config.timeoutIntervalForResource = 24*3600; config.waitsForConnectivity = !testing
        session = URLSession(configuration: config, delegate: delegate, delegateQueue: nil)
        #if !targetEnvironment(macCatalyst)
        if !testing {
            let hlsConfig = URLSessionConfiguration.background(withIdentifier: identifier + ".hls")
            hlsConfig.isDiscretionary = false
            assets = AVAssetDownloadURLSession(configuration: hlsConfig, assetDownloadDelegate: delegate, delegateQueue: .main)
        }
        #endif
        Task { await restore() }
    }

    func handleBackground(_ identifier: String, completion: @escaping () -> Void) { handlers[identifier] = completion }
    private func resumeURL(_ id: UUID) -> URL { support.appendingPathComponent(id.uuidString + ".resume") }
    func localURL(_ record: DownloadRecord) -> URL? {
        guard let path = record.localPath, let url = SourceRules.ownedURL(relativePath: path, root: home), FileManager.default.fileExists(atPath: url.path) else { return nil }
        return url
    }
    private func relative(_ url: URL) -> String? {
        SourceRules.relativePath(of: url, root: home)
    }
    private func save() {
        do {
            let data = try JSONEncoder().encode(records)
            try data.write(to: support.appendingPathComponent("downloads.json"), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            lastSave = Date()
        } catch { message = "保存任务记录失败：" + error.localizedDescription }
    }
    private func restore() async {
        let fileTasks = await session.allTasks
        let assetTasks = await assets?.allTasks ?? []
        for task in fileTasks + assetTasks {
            guard let id = task.taskDescription.flatMap(UUID.init(uuidString:)), let index = records.firstIndex(where: { $0.id == id }), records[index].status != .completed else { task.cancel(); continue }
            tasks[id] = task; records[index].taskID = task.taskIdentifier
            if task.state == .running { records[index].status = .downloading }
        }
        for index in records.indices where tasks[records[index].id] == nil {
            records[index].taskID = nil
            if [.resolving, .downloading, .pausing].contains(records[index].status) { records[index].status = .paused; records[index].note = "上次下载已停止，点继续恢复" }
        }
        save(); pump()
    }
    func enqueue(_ source: MediaSource) {
        guard records.count < SourceRules.recordLimit else { message = "任务记录已达 500 项，请先删除不需要的任务"; return }
        if records.contains(where: { ($0.source.url == source.url || source.originURL != nil && $0.source.originURL == source.originURL) && [.queued, .resolving, .downloading, .pausing, .paused].contains($0.status) }) { message = "此链接已有任务，请继续原任务"; return }
        records.insert(DownloadRecord(source: source, expected: source.size), at: 0)
        save(); pump()
    }
    private func pump() {
        var running = records.filter { [.downloading, .pausing].contains($0.status) }.count
        for index in records.indices.reversed() where records[index].status == .queued && running < 2 {
            let record = records[index]
            do {
                let task: URLSessionTask
                if let existing = tasks[record.id] { task = existing }
                else if record.source.kind == .hls {
                    if let previous = localURL(record) {try? FileManager.default.removeItem(at:previous);records[index].localPath=nil}
                    guard let assetTask = assets?.makeAssetDownloadTask(asset: AVURLAsset(url: record.source.url), assetTitle: record.source.title, assetArtworkData: nil, options: nil) else { throw SourceError("此设备暂时无法创建 HLS 离线任务，请用 iPhone 真机或改用 MP4 链接") }
                    task = assetTask
                } else if let size = try? resumeURL(record.id).resourceValues(forKeys: [.fileSizeKey]).fileSize, size <= 4*1024*1024, let data = try? Data(contentsOf: resumeURL(record.id)) {
                    task = session.downloadTask(withResumeData: data)
                } else { task = session.downloadTask(with: record.source.downloadRequest) }
                task.taskDescription = record.id.uuidString
                tasks[record.id] = task; records[index].taskID = task.taskIdentifier
                records[index].status = .downloading; records[index].note = ""
                task.resume(); running += 1
            } catch { records[index].status = .failed; records[index].note = error.localizedDescription }
        }
        save()
    }
    func pause(_ id: UUID) {
        guard let index = records.firstIndex(where: { $0.id == id }), [.queued, .resolving, .downloading].contains(records[index].status) else { return }
        if records[index].status == .resolving {
            resolving.removeValue(forKey:id)?.cancel(); records[index].status = .paused; save(); return
        }
        if let task = tasks[id] as? URLSessionDownloadTask {
            records[index].status = .pausing; save()
            task.cancel { [weak self] data in Task { @MainActor in
                guard let self, let index = self.records.firstIndex(where: { $0.id == id }), self.records[index].status == .pausing else { return }
                if let data, data.count <= 4*1024*1024 { try? data.write(to: self.resumeURL(id), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication]) }
                else { self.records[index].note = "来源未提供续传信息，继续时将重新开始" }
                self.records[index].status = .paused; self.records[index].taskID = nil
                self.tasks.removeValue(forKey: id); self.save(); self.pump()
            } }
        } else { tasks[id]?.suspend(); records[index].status = .paused; save(); pump() }
    }
    func resume(_ id: UUID) {
        guard let index = records.firstIndex(where: { $0.id == id }), [.paused, .failed].contains(records[index].status) else { return }
        let source=records[index].source
        if BilibiliLink.isPlatformURL(source.url) || source.originURL != nil && records[index].status == .failed {
            guard resolving.count < 2 else {message="正在识别其他视频，请稍后再试";return}
            records[index].status = .resolving; records[index].note=""; save()
            resolving[id]=Task { [weak self] in
                guard let self else {return}
                do {
                    let results=try await MediaResolver(allowLocalHTTP:self.testing).resolve(source.originURL ?? source.url)
                    guard !Task.isCancelled, let current=self.records.firstIndex(where:{$0.id == id}), self.records[current].status == .resolving else {return}
                    guard let refreshed=results.first(where:{$0.kind == .video}) else {throw SourceError("来源未提供可下载的视频")}
                    // Signed CDN links expire. Resume data for the previous URL must not be reused.
                    try? FileManager.default.removeItem(at:self.resumeURL(id))
                    self.records[current].source=refreshed; self.records[current].received=0;self.records[current].expected=refreshed.size
                    self.records[current].fraction=0;self.records[current].status = .queued
                    self.resolving.removeValue(forKey:id);self.save();self.pump()
                } catch {
                    guard !Task.isCancelled, let current=self.records.firstIndex(where:{$0.id == id}), self.records[current].status == .resolving else {return}
                    self.records[current].status = .failed;self.records[current].note=error.localizedDescription
                    self.resolving.removeValue(forKey:id);self.save()
                }
            }
            return
        }
        records[index].status = .queued; save(); pump()
    }
    func remove(_ id: UUID) {
        guard let record = records.first(where: { $0.id == id }) else { return }
        records.removeAll { $0.id == id }; tasks.removeValue(forKey: id)?.cancel(); resolving.removeValue(forKey:id)?.cancel(); lastProgress.removeValue(forKey: id)
        if let file = localURL(record) { try? FileManager.default.removeItem(at: file) }
        let folder = documents.appendingPathComponent("Downloads/" + id.uuidString)
        if (try? FileManager.default.contentsOfDirectory(atPath:folder.path).isEmpty) == true {try? FileManager.default.removeItem(at:folder)}
        try? FileManager.default.removeItem(at: resumeURL(id)); save(); pump()
    }
    func receive(_ event: TransferEvent) {
        if case .eventsFinished(let identifier) = event { save(); handlers.removeValue(forKey: identifier)?(); return }
        let id: UUID, taskID: Int
        switch event {
        case .progress(let a, let b, _, _, _), .file(let a, let b, _, _), .hlsLocation(let a, let b, _), .ended(let a, let b, _, _): id = a; taskID = b
        case .eventsFinished: return
        }
        guard let index = records.firstIndex(where: { $0.id == id }) else {
            if case .file(_, _, let file, _) = event, file.path.hasPrefix(documents.appendingPathComponent("Downloads/"+id.uuidString).path+"/") {try? FileManager.default.removeItem(at:file)}
            if case .hlsLocation(_, _, let file) = event, let path=relative(file), SourceRules.ownedURL(relativePath:path,root:home) != nil, file.pathExtension == "movpkg" {try? FileManager.default.removeItem(at:file)}
            return
        }
        guard records[index].taskID == taskID, records[index].status != .completed else {return}
        switch event {
        case .progress(_, _, let received, let expected, let fraction):
            guard records[index].status == .downloading, Date().timeIntervalSince(lastProgress[id] ?? .distantPast) >= 0.5 else { return }
            lastProgress[id] = Date(); records[index].received = max(0, received); records[index].expected = max(0, expected)
            records[index].fraction = fraction ?? (expected > 0 ? min(0.99, Double(received)/Double(expected)) : 0)
            if Date().timeIntervalSince(lastSave) >= 3 { save() }
        case .file(_, _, let file, let count):
            var saved=file
            if records[index].source.originURL != nil {
                let named=file.deletingLastPathComponent().appendingPathComponent(SourceRules.fileName(records[index].source.title))
                if named != file {
                    do {try FileManager.default.moveItem(at:file,to:named);saved=named} catch { /* Retain the verified file under its original name. */ }
                }
            }
            records[index].localPath = relative(saved); records[index].received = count; records[index].expected = count
            records[index].fraction = 1; records[index].status = .completed; records[index].taskID = nil
            tasks.removeValue(forKey: id); try? FileManager.default.removeItem(at: resumeURL(id)); save(); pump()
        case .hlsLocation(_, _, let file):
            records[index].localPath = relative(file); save()
        case .ended(_, _, let error, let data):
            if records[index].status == .pausing || records[index].status == .paused && records[index].source.kind != .hls { return }
            if let error {
                if let data, data.count <= 4*1024*1024 { try? data.write(to: resumeURL(id), options: .atomic) }
                records[index].status = .failed; records[index].note = error.localizedDescription
            } else if records[index].source.kind == .hls && localURL(records[index]) != nil {
                records[index].status = .completed; records[index].fraction = 1
                records[index].note = "系统离线视频，可在拾流内播放"
            } else { records[index].status = .failed; records[index].note = "未取得完整文件，请重试" }
            records[index].taskID = nil; tasks.removeValue(forKey: id); save(); pump()
        case .eventsFinished: break
        }
    }
}
