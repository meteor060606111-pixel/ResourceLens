# 拾流 · 独立 iPhone 版 0.1.1

SwiftUI 原生 App，下载在 iPhone 本机执行、文件保存在 iPhone。使用时不连接 Mac，不扫描配对码，也不需要运行电脑上的拾流。**手机端没有音乐搜索、歌单、音乐下载或音乐运行时。** 桌面 1.8.0 继续独立维护。

## 已实现

- 粘贴视频、图片、文件直链或分享文本。读取公开网页里的 video / source / Open Graph / JSON-LD 媒体地址；网页结果先展示类型并由用户选择保存。
- HTTPS 普通文件使用系统 URLSession 后台下载，最多同时两个任务。暂停时保存来源提供的续传数据；不支持续传的来源会提示重新开始。系统进程负责传输，媒体不整体加载为 Data。
- HLS 使用 AVAssetDownloadURLSession 保存在系统离线视频包中，由 AVPlayer 播放。普通 MP4、图片与文件可分享至「文件」，兼容的视频和图片可通过仅添加照片权限保存到相册。
- 原生任务列表、全局搜索、已保存筛选、本地图片 / 文件预览、视频播放、删除任务和对应文件。预览关闭后释放播放器。
- 识别 HTML 登录页、HTTP 错误、空文件及不匹配的视频头，避免把网页或错误内容当成视频完成。

## 轻量约束

无第三方包依赖，不包含 Flutter、WebView、Python、Node.js、yt-dlp 或 FFmpeg。当前 Release 真机 `.app` 约 1.3 MiB、未签名 IPA 约 0.5 MB，精确字节见 `release/ios.noindex/footprint.json`。

构建对 App 设置 **8 MiB 硬上限**。任务元数据上限 500 项，列表按系统方式复用，进度最多每 0.5 秒更新，任务记录最多每 3 秒随进度保存一次；网页探测最多 1 MiB，读取到媒体响应头即取消探测。播放器仅在预览时创建。文件大小不计入安装包。iPhone 13 / Release 真机静置约 22–26 MiB，本次短视频预览峰值约 65 MiB；测量场景与限制见 [真机验收记录](VALIDATION.md)。

## 本机与 CI 构建

需要完整 Xcode 16+、iOS 17+ SDK。项目已提交，不需要 CocoaPods、XcodeGen 或 npm：

```sh
swift test --package-path ios
xcodebuild -project ios/ResourceLens.xcodeproj -scheme ResourceLensPhone \
  -configuration Release -destination 'generic/platform=iOS' \
  -derivedDataPath release/ios-build.noindex CODE_SIGNING_ALLOWED=NO build
python3 ios/scripts/package.py \
  --app release/ios-build.noindex/Build/Products/Release-iphoneos/ResourceLensPhone.app \
  --out release/ios.noindex
```

`generate-project.py` 只用 Python 标准库重建 Xcode 项目，CI 验证生成结果一致。`ios.yml` 运行核心测试、真机构建、体积检查、iPhone 模拟器上的真实界面与文件字节验证。测试网关仅替代文件来源，不提供下载引擎；发布 App 没有电脑服务地址。Debug 构建的本地 HTTP 只供隔离测试，Release 保持系统 ATS，产品只承诺 HTTPS。

## 安装到 iPhone

当前产物是 **未签名开发预览 IPA**，不能直接点击安装，也不是已上架或 TestFlight 构建。打开 `ios/ResourceLens.xcodeproj`，在 Signing & Capabilities 选择自己的 Team、连接解锁并信任的 iPhone，选择 ResourceLensPhone 后运行。首次开发安装与签名需要 Mac / Xcode；安装后实际使用和下载不需要电脑。个人开发签名的有效期及分发范围以 Apple 账号条件为准。

已在 iPhone 13 完成开发签名安装、公开 HTTPS 下载、文件字节核对、视频 / 图片预览、图片保存到照片和重新启动后的任务保留。CI 的 iPhone 模拟器 UI 与文件字节核对亦通过，详见 [真机验收记录](VALIDATION.md)。当前本机开发签名有效至 2026-10-23；这不是公开分发证书。

## 当前能力边界

- 首版没有把桌面的 B 站、抖音等专用解析器搬进来。只有平台实际暴露可直接读取的媒体地址时才能保存；登录、加密播放和动态脚本内的媒体不保证支持。
- HLS 保存为系统离线包，并不转换成 MP4，也不能直接保存到照片。HLS 真机后台行为仍待验收；直播不在首版承诺范围内。
- 后台传输受 iOS 调度影响。用户从多任务界面主动划掉 App 后，系统会停止后台传输；重新打开后可恢复任务。不能承诺锁屏后无限运行。
- 相册权限仅在用户点击保存时申请；未申请读取整个照片图库的权限。

实现参考 Apple 的 [后台下载](https://developer.apple.com/documentation/foundation/downloading-files-in-the-background)、[HLS 离线存储](https://developer.apple.com/documentation/avfoundation/offline-playback-and-storage) 与 [HLS 文件位置规则](https://developer.apple.com/documentation/avfoundation/avassetdownloaddelegate/urlsession(_:assetdownloadtask:didfinishdownloadingto:))。下载逻辑为本项目新写的 Swift 代码，没有复制 Gopeed 的 GPL 引擎。
