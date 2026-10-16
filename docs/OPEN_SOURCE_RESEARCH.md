# 开源源码研究与拾流重构记录

分析日期：2026-10-16。参考源码为浅克隆，提交固定在下表；结论来自入口、调用链和文件保存路径，不把 README 的“支持手机”当成独立下载证据。本轮没有手机硬件验收。

## 结论：手机直接下载与遥控电脑是两条路径

| 项目 / 使用形态 | 下载引擎实际运行处 | 文件首先保存在哪里 | 手机与电脑的关系 |
| --- | --- | --- | --- |
| Gopeed 原生 iPhone / Android 客户端 | 手机进程里的 Go libgopeed，通过 Flutter MethodChannel 调用 | iOS 的 Documents；Android 外部存储应用目录或 Documents 回退 | 手机可以自己执行下载，默认不依赖 Mac |
| Gopeed Web 界面 | Web 页面连接的 Gopeed 后端进程 | 后端所在设备的下载目录 | 手机浏览器可充当远程面板；这不是原生手机客户端下载到手机 |
| AB Download Manager Android 客户端 | Android 本地注入的 HTTP / HLS 下载器与 DownloadManager | Android 的公共 Downloads 目录 | 手机直接下载，不需要把 Mac 作为下载引擎 |
| AB 桌面浏览器集成 | 收到 API 请求的桌面 DownloadManager | 桌面设备的下载目录 | 扩展发送链接到桌面，不意味着 Android 客户端依赖桌面 |
| 当前拾流 iPhone 网页伴侣 | Mac 的 Engine、yt-dlp、FFmpeg | Mac 下载目录，再通过已配对的 /files 路由提供文件 | iPhone 发起、暂停、续传和预览；要在手机保留文件需再保存一份，Mac 必须保持运行 |

### 可复核的源码证据

- Gopeed iOS：[Flutter 调用 LibgopeedStart / Invoke](https://github.com/GopeedLab/gopeed/blob/eee39e012f9681983ebe61e7549b5facb9453b77/ui/flutter/ios/Runner/AppDelegate.swift#L26)；[手机本地保存目录](https://github.com/GopeedLab/gopeed/blob/eee39e012f9681983ebe61e7549b5facb9453b77/ui/flutter/lib/app/application/app_runtime_controller.dart#L376)。
- Gopeed Android：[MethodChannel 调用本地 Libgopeed](https://github.com/GopeedLab/gopeed/blob/eee39e012f9681983ebe61e7549b5facb9453b77/ui/flutter/android/app/src/main/kotlin/com/gopeed/gopeed/MainActivity.kt#L23)。
- Gopeed Web：[Dio 请求后端，并发送 X-Api-Token](https://github.com/GopeedLab/gopeed/blob/eee39e012f9681983ebe61e7549b5facb9453b77/ui/flutter/lib/core/network/gopeed/entry/gopeed_transport_web.dart#L7)。Web 的目标地址由客户端配置决定，文件由被连接后端保存。
- AB Android：[本地 HTTP / HLS 引擎与 DownloadManager 注入](https://github.com/amir1376/ab-download-manager/blob/e329495b6630b18e4b41d7682da33318b0f9ea18/android/app/src/main/kotlin/com/abdownloadmanager/android/di/Di.kt#L206)；[公共 Downloads 路径](https://github.com/amir1376/ab-download-manager/blob/e329495b6630b18e4b41d7682da33318b0f9ea18/shared/app/src/androidMain/kotlin/com/abdownloadmanager/shared/util/downloadlocation/PlatformDownloadLocationProvider.android.kt#L7)；[本地前台下载服务](https://github.com/amir1376/ab-download-manager/blob/e329495b6630b18e4b41d7682da33318b0f9ea18/android/app/src/main/kotlin/com/abdownloadmanager/android/service/DownloadSystemService.kt#L24)。该固定提交的公开支持平台包含 Android，没有在该源码中找到已交付的 iOS 客户端。
- AB 浏览器接口：[API key、add、queues 与 start-headless-download](https://github.com/amir1376/ab-download-manager/blob/e329495b6630b18e4b41d7682da33318b0f9ea18/integration/server/src/main/kotlin/com/abdownloadmanager/integration/Endpoints.kt#L26)，这是桌面集成接口，不能当作手机独立下载的实现证据。
- 拾流：[手机请求调用 Mac Engine.startDownloadOptions](https://github.com/meteor060606111-pixel/ResourceLens/blob/d6bb7d809c047d386512dbb497ef5a96527ce6ad/app/mobile.go#L415)；[仅提供 Mac 已完成且位于下载目录内的文件](https://github.com/meteor060606111-pixel/ResourceLens/blob/d6bb7d809c047d386512dbb497ef5a96527ce6ad/app/mobile.go#L448)。源码对应现有 1.6.4 基线；本轮暂不改手机 UI 或配对协议。

Gopeed iOS 源码也单独处理后台任务与 Live Activity；因此即便有本地下载引擎，也不能推导出 iPhone 锁屏后可无限期运行。后续若做独立 iOS 版，需分别验收系统后台传输、媒体合并、分享保存与沙盒目录。当前用户选择保持“连接 Mac”的方向，这轮仅调研。

## 本轮落地范围

- **已取消：浏览器扩展 / 一键发送到桌面。** 保留参考仓库用于研究，不把扩展或相应服务加入拾流。
- **Mac 更新与副本治理：** 默认从官方 GitHub Releases 挑选本机芯片对应的稳定 macOS 安装包；核对 GitHub SHA-256、包内应用标识、版本、Mach-O 芯片与签名完整性。用户点击安装后退出，先在 Applications 同一文件系统暂存，再原子替换；验证失败恢复原应用，成功后删除旧程序和已用暂存安装包并重新打开。现有配置和下载目录不进入替换事务。已存在 Developer ID 团队时，新包必须保持同一团队。
- **下载队列：** 一个加锁的调度决策负责名额；已运行任务不被抢占，待下载按优先级与加入时间排序。预约任务不占下载名额；支持暂停后续队列。HTTP 所有分段共享单任务限速，yt-dlp 限速时使用一个片段连接，避免并发片段成倍超出限额。暂停/取消会终止限速等待。
- **窗口与视觉：** 保留现有 AppKit 原生标题栏与拖动实现；新增队列控件和计划对话框复用当前玻璃样式。
- **构建清理：** 构建放在 `.noindex` 目录；成功打包后清除应用副本、DMG 暂存目录和图标目录，并移除该输出目录中同一芯片的旧版安装包。只保留 `/Applications/ResourceLens.app` 作为安装应用。

## 借鉴边界与后续方向

| 项目 | 借鉴内容 | 手机结论 / 本轮处理 |
| --- | --- | --- |
| Gopeed | 前后端边界、原生手机引擎与 Web 远程模式的区分 | 保留调研，未迁移 Flutter 或复制 GPL 引擎 |
| Gopeed browser-extension | 发送链接与配置服务地址的交互 | 本提交未找到明确的根许可文件；不复制代码；功能已取消 |
| AB Download Manager | 下载队列、调度、限速及 Android 本地下载的结构 | 参考行为，从零实现适合 Go 现有引擎的调度 |
| AB browser integration | 桌面 API 边界 | 功能已取消 |
| Sparkle | 更新包校验、同卷替换、失败恢复、成功后清理 | macOS 更新框架，不是手机连接库；本轮未嵌入 Sparkle 二进制 |
| Parabolic | yt-dlp 的画质、字幕和下载后处理组织 | 桌面功能参考，没有把它当成手机端方案 |
| ytdl-sub | 订阅 / 频道归档、命名与媒体库整理 | 后续候选，不在本轮增加常驻订阅任务 |
| Mutagen | 音频标签、封面与元数据处理 | 已在拾流音乐运行时中，后续可复用 |
| LosslessCut | 下载后的无损裁切、提取音轨与重封装 | 后续候选，不照搬 Electron UI |

Sparkle 的完整发行体系包含独立 Ed25519 签名密钥和 appcast，并推荐 Developer ID 签名及公证。本轮的 GitHub 校验 + 显式安装流程不等同接入 Sparkle，也不等同 Apple 公证；现有应用仍为 ad-hoc 签名。要继续做无人值守安装或完整 Sparkle 迁移，需要先完成发行签名与密钥保管安排。参考：[Sparkle 官方接入与签名说明](https://sparkle-project.org/documentation/)。

## 固定源码版本与许可

| 本地目录 | 仓库 | 固定提交 | 许可 |
| --- | --- | --- | --- |
| `gopeed` | [GopeedLab/gopeed](https://github.com/GopeedLab/gopeed) | `eee39e012f9681983ebe61e7549b5facb9453b77` | GPL-3.0 |
| `gopeed-browser-extension` | [GopeedLab/browser-extension](https://github.com/GopeedLab/browser-extension) | `d29bf1b0a7acfe233c3b13e388bb45939133ea48` | 本提交未找到明确根许可文件 |
| `ab-download-manager` | [amir1376/ab-download-manager](https://github.com/amir1376/ab-download-manager) | `e329495b6630b18e4b41d7682da33318b0f9ea18` | Apache-2.0 |
| `ab-browser-extension` | [amir1376/ab-download-manager-browser-integration](https://github.com/amir1376/ab-download-manager-browser-integration) | `658a0c70188d78033f478ba51c102a273322166f` | Apache-2.0 |
| `sparkle` | [sparkle-project/Sparkle](https://github.com/sparkle-project/Sparkle) | `b41aa91142bb752324838a3c4e0cac2e5f942ff4` | 自带许可及第三方条款，以 LICENSE 为准 |
| `parabolic` | [NickvisionApps/Parabolic](https://github.com/NickvisionApps/Parabolic) | `1118e6a3abbf5ffc1f764c4c9781f98d4c0482d4` | MIT |
| `ytdl-sub` | [jmbannon/ytdl-sub](https://github.com/jmbannon/ytdl-sub) | `3fc2af007b2393f1150e747e9105af0f4a366dbf` | GPL-3.0 |
| `mutagen` | [quodlibet/mutagen](https://github.com/quodlibet/mutagen) | `ada28b2cc92c515f3f26640a6feef6516d195872` | GPL-2.0+ |
| `lossless-cut` | [mifi/lossless-cut](https://github.com/mifi/lossless-cut) | `2610158c7861e0b96e2020cebcdd3c539ff33830` | GPL-2.0 |
| `ResourceLens` | [meteor060606111-pixel/ResourceLens](https://github.com/meteor060606111-pixel/ResourceLens) | `d6bb7d809c047d386512dbb497ef5a96527ce6ad` | MIT；内置引擎另见各自许可 |

许可记录用于区分直接复用与行为参考；本轮没有复制参考项目的实现代码。精确本地路径见 `analysis/repositories.json`。


## 本轮最终交付补充

用户后续明确关注应用和安装包的磁盘体积。1.7.2 在保留完整音乐与视频引擎的前提下，将 Apple 芯片应用实际占用从 684.1 MiB 降至 409.8 MiB（减少 40.1%），DMG 从 343.5 MiB 降至 225.7 MiB（减少 34.3%）。完整视频回归与 Go 竞态检测通过。实现和平台边界见 [macOS 验证记录](../installer/macos/VERIFICATION.md)。
