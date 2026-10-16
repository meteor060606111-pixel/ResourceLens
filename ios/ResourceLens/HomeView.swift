import SwiftUI
import AVKit
import QuickLook
import Photos

struct HomeView: View {
    @EnvironmentObject private var store: DownloadStore
    @State private var adding = false
    @State private var information = false
    @State private var search = ""
    @State private var filesOnly = false
    @State private var removing: DownloadRecord?
    @State private var preview: URL?
    @State private var movie: DownloadRecord?
    private var shown: [DownloadRecord] {
        store.records.filter { (!filesOnly || $0.status == .completed) && (search.isEmpty || $0.source.title.localizedCaseInsensitiveContains(search)) }
    }
    var body: some View {
        NavigationStack {
            Group {
                if store.records.isEmpty { empty }
                else {
                    List {
                        Section {
                            Picker("显示任务", selection:$filesOnly) { Text("全部任务").tag(false); Text("已保存").tag(true) }.pickerStyle(.segmented).listRowBackground(Color.clear)
                        }.listRowSeparator(.hidden)
                        if shown.isEmpty { ContentUnavailableView.search(text: search) }
                        ForEach(shown) { record in
                            DownloadCard(record:record, local:store.localURL(record), pause:{store.pause(record.id)}, resume:{store.resume(record.id)}, preview:{
                                if record.source.kind == .video || record.source.kind == .hls { movie = record }
                                else { preview = store.localURL(record) }
                            }, photos:{ saveToPhotos(record) }, remove:{removing=record})
                            .listRowSeparator(.hidden).listRowBackground(Color.clear).listRowInsets(EdgeInsets(top:6,leading:16,bottom:6,trailing:16))
                        }
                    }
                    .listStyle(.plain).scrollContentBackground(.hidden)
                    .searchable(text:$search, prompt:"搜索下载和文件")
                }
            }
            .background(Color(uiColor:.systemGroupedBackground))
            .navigationTitle("拾流").navigationBarTitleDisplayMode(.large)
            .toolbar {
                ToolbarItem(placement:.topBarLeading) { Button {information=true} label:{Image(systemName:"info.circle")}.accessibilityLabel("使用说明") }
                ToolbarItem(placement:.topBarTrailing) { Button {adding=true} label:{Image(systemName:"plus")}.accessibilityLabel("新建下载") }
            }
            .sheet(isPresented:$adding) { AddDownloadView() }
            .sheet(isPresented:$information) { informationView }
            .sheet(item:$movie) { record in
                if let url=store.localURL(record) { MovieView(url:url,title:record.source.title) }
                else { ContentUnavailableView("文件已移动或删除",systemImage:"doc.badge.ellipsis") }
            }
            .quickLookPreview($preview)
            .alert("提示",isPresented:Binding(get:{store.message != nil},set:{if !$0 {store.message=nil}})) { Button("知道了",role:.cancel){store.message=nil} } message:{Text(store.message ?? "")}
            .confirmationDialog("删除任务及已下载文件？",isPresented:Binding(get:{removing != nil},set:{if !$0{removing=nil}}),titleVisibility:.visible) {
                Button("删除",role:.destructive){if let record=removing{store.remove(record.id)};removing=nil}
                Button("取消",role:.cancel){removing=nil}
            } message:{ Text("只删除这一个任务在拾流中保存的文件。") }
        }
    }
    private var empty: some View {
        VStack(spacing:22) {
            Spacer()
            Image(systemName:"water.waves").font(.system(size:64,weight:.medium)).foregroundStyle(.tint).accessibilityHidden(true)
            Text("把喜欢的内容，\n存在手机里").font(.system(size:30,weight:.bold)).multilineTextAlignment(.center)
            Text("iPhone 独立下载，无需连接电脑").font(.subheadline).foregroundStyle(.secondary)
            Button {adding=true} label:{Label("添加下载链接",systemImage:"plus").frame(maxWidth:.infinity).padding(.vertical,9)}
                .buttonStyle(.borderedProminent).controlSize(.large).padding(.horizontal,32)
            Text("视频 · 图片 · 文件").font(.footnote).foregroundStyle(.secondary)
            Spacer();Spacer().frame(height:30)
        }.frame(maxWidth:.infinity)
    }
    private var informationView: some View {
        NavigationStack {
            List {
                Section("独立 iPhone 版") {
                    Label("文件直接保存到这台 iPhone",systemImage:"iphone")
                    Label("使用时不需要电脑或配对码",systemImage:"checkmark.circle")
                    Text("支持 HTTPS 视频、图片、文件直链，以及网页中公开的媒体地址。音乐搜索和音乐下载未加入手机版。")
                }
                Section("下载与保存") {
                    Text("系统后台下载会保留任务。主动从多任务界面划掉拾流后，系统会停止后台传输；再次打开后可继续。")
                    Text("普通文件可分享至「文件」，兼容的视频和图片可保存到照片。HLS 保存为系统离线视频包，在拾流内播放。")
                    Text("需要登录、平台专用解析或授权的链接可能无法下载。HTML 网页不会被保存成 MP4。")
                }
                Section { Text("ResourceLens iPhone 0.1.1 · 原生轻量版").font(.footnote).foregroundStyle(.secondary) }
            }.navigationTitle("使用说明").navigationBarTitleDisplayMode(.inline)
                .toolbar {ToolbarItem(placement:.confirmationAction){Button("完成"){information=false}}}
        }.presentationDetents([.large])
    }
    private func saveToPhotos(_ record: DownloadRecord) {
        guard let url=store.localURL(record), [.image,.video].contains(record.source.kind) else {return}
        Task {
            let status=await PHPhotoLibrary.requestAuthorization(for:.addOnly)
            guard status == .authorized || status == .limited else {store.message="需要允许添加照片，或使用分享保存到「文件」";return}
            do {
                try await PHPhotoLibrary.shared().performChanges {
                    if record.source.kind == .video { PHAssetChangeRequest.creationRequestForAssetFromVideo(atFileURL:url) }
                    else {PHAssetCreationRequest.forAsset().addResource(with:.photo,fileURL:url,options:nil)}
                }
                store.message="已保存到照片"
            } catch {store.message="此文件暂不能存入照片，可使用分享保存到「文件」"}
        }
    }
}

private struct DownloadCard: View {
    let record:DownloadRecord
    let local:URL?
    let pause:()->Void, resume:()->Void, preview:()->Void, photos:()->Void, remove:()->Void
    var body:some View {
        VStack(alignment:.leading,spacing:13) {
            HStack(alignment:.top,spacing:12) {
                Image(systemName:record.source.kind.symbol).font(.title3).foregroundStyle(.tint).frame(width:44,height:48).background(.tint.opacity(0.08),in:RoundedRectangle(cornerRadius:12))
                VStack(alignment:.leading,spacing:6) {
                    Text(record.source.title).font(.headline).lineLimit(2).textSelection(.enabled)
                    Text(record.source.url.host ?? "").font(.caption).foregroundStyle(.secondary).lineLimit(1)
                    Text(record.status.label).font(.caption.weight(.medium)).foregroundStyle(record.status == .completed ? Color.teal : .secondary)
                }
                Spacer(minLength:0)
                Menu {Button("删除任务和文件",role:.destructive,action:remove)} label:{Image(systemName:"ellipsis").frame(minWidth:30,minHeight:44)}.accessibilityLabel("任务选项")
            }
            if record.status == .downloading || record.status == .paused || record.status == .pausing {
                if record.expected > 0 || record.source.kind == .hls {ProgressView(value:record.fraction)} else {ProgressView()}
            }
            if record.expected>0 {Text(ByteCountFormatter.string(fromByteCount:record.received,countStyle:.file)+" / "+ByteCountFormatter.string(fromByteCount:record.expected,countStyle:.file)).font(.caption).foregroundStyle(.secondary)}
            HStack {
                Spacer()
                if record.status == .downloading || record.status == .queued {Button("暂停",action:pause).buttonStyle(.bordered)}
                if record.status == .paused || record.status == .failed {Button(record.status == .failed ? "重试":"继续",action:resume).buttonStyle(.borderedProminent)}
                if record.status == .completed, let local {
                    Button(record.source.kind == .hls || record.source.kind == .video ? "播放":"预览",action:preview).buttonStyle(.bordered)
                    if record.source.kind != .hls {ShareLink(item:local){Image(systemName:"square.and.arrow.up")}.buttonStyle(.bordered).accessibilityLabel("分享文件")}
                    if [.image,.video].contains(record.source.kind) {Button(action:photos){Image(systemName:"square.and.arrow.down")}.buttonStyle(.bordered).accessibilityLabel("保存到照片")}
                }
            }.controlSize(.regular)
            if !record.note.isEmpty {Text(record.note).font(.caption).foregroundStyle(.secondary)}
            if record.status == .completed && local == nil {Text("文件已移动或删除，可重新添加链接").font(.caption).foregroundStyle(.secondary)}
        }.padding(16).background(.background,in:RoundedRectangle(cornerRadius:20))
            .accessibilityElement(children:.contain).accessibilityIdentifier("download-card")
    }
}

private struct AddDownloadView:View {
    @EnvironmentObject private var store:DownloadStore
    @Environment(\.dismiss) private var dismiss
    @State private var text=""
    @State private var reading=false
    @State private var error=""
    @State private var candidates:[MediaSource]=[]
    @State private var request:Task<Void,Never>?
    var body:some View {
        NavigationStack {
            Form {
                Section("粘贴链接或分享文本") {
                    TextEditor(text:$text).frame(minHeight:120).accessibilityIdentifier("download-input")
                    PasteButton(payloadType:String.self){strings in text=strings.joined(separator:"\n").prefix(16384).description}.disabled(reading)
                }
                Section {
                    Button {read()} label:{HStack {if reading{ProgressView()};Text(reading ? "正在读取媒体…":"识别并下载").frame(maxWidth:.infinity)}}
                        .disabled(reading||text.trimmingCharacters(in:.whitespacesAndNewlines).isEmpty)
                    Text("支持 HTTPS 视频、图片、文件直链，以及包含公开媒体地址的网页。文件保存在 iPhone。").font(.footnote).foregroundStyle(.secondary)
                    if !error.isEmpty {Text(error).font(.footnote).foregroundStyle(.red).accessibilityIdentifier("source-error")}
                }
                if !candidates.isEmpty {
                    Section("选择要保存的内容") {
                        ForEach(candidates){source in Button {store.enqueue(source);dismiss()} label:{VStack(alignment:.leading,spacing:5){Text(source.title).lineLimit(2);Text(source.kind.label).font(.caption).foregroundStyle(.secondary)}}}
                    }
                }
            }.navigationTitle("新建下载").navigationBarTitleDisplayMode(.inline)
                .toolbar {ToolbarItem(placement:.cancellationAction){Button("取消"){request?.cancel();dismiss()}}}
        }.onDisappear{request?.cancel()}
    }
    private func read() {
        reading=true;error="";candidates=[]
        request=Task {
            defer {reading=false}
            do {
                let url=try SourceRules.inputURL(text)
                let results=try await MediaResolver(allowLocalHTTP:store.testing).resolve(url)
                guard !Task.isCancelled else {return}
                if results.count == 1, results[0].url == url, (try? SourceRules.kind(url:url)) != nil {store.enqueue(results[0]);dismiss()} else {candidates=results}
            }catch{if !Task.isCancelled{self.error=error.localizedDescription}}
        }
    }
}

private struct MovieView:View {
    let url:URL,title:String
    @Environment(\.dismiss) private var dismiss
    @State private var player:AVPlayer?
    var body:some View {
        NavigationStack {
            VideoPlayer(player:player).navigationTitle(title).navigationBarTitleDisplayMode(.inline)
                .toolbar{ToolbarItem(placement:.confirmationAction){Button("完成"){dismiss()}}}
        }.onAppear{player=AVPlayer(url:url)}.onDisappear{player?.pause();player?.replaceCurrentItem(with:nil);player=nil}
    }
}
