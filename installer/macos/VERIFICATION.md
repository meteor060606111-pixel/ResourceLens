# macOS 1.7.2 验证记录

验证日期：2026-10-16；本机为 Apple Silicon，macOS 27.0.1，完整 Xcode 27。最低系统要求保持 macOS 14。

## 体积与功能边界

| 项目 | 精简前 | 1.7.2 | 减少 |
| --- | --- | --- | --- |
| 应用实际占用（du） | 684.1 MiB | 409.8 MiB | 40.1% |
| Apple 芯片 DMG | 343.5 MiB | 225.7 MiB | 34.3% |

通过共用 Node.js、删除开发工具与头文件、Tk 示例、未被动态依赖的 CPython 嵌入库和非必需符号实现。保留业务依赖、数据、许可证、57 个平台注册与 24 个歌单实现。PyInstaller 的 yt-dlp 包含尾部内嵌档案，必须原样保留，不能使用 strip；构建检查已覆盖所有媒体工具启动。详细字节数在应用 `Contents/Resources/runtime-footprint.json`。

## 已执行验证

- 已在真实 `/Applications/ResourceLens.app` 从旧版原位升级并自动重启至 1.7.2。校验失败恢复的目录交换测试通过；旧应用未退出时的超时保护也保留了原应用。
- 安装后更新缓存无 DMG、worker 或旧应用残留；工作区构建输出无 `.app`，仅保留当前 arm64 DMG。Spotlight 按 bundle id 只返回 `/Applications/ResourceLens.app`。
- `codesign --verify --deep --strict`、Info.plist 检查和 DMG 校验通过。当前仍为 ad-hoc 签名，未使用 Developer ID 或 Apple 公证，不能视作已具备公证发行资质。
- 安装后的精简 Python、共用 Node 与媒体工具用于完整 `go test -race -tags desktop,production ./app ./frontend -count=1`，通过，app 测试约 51.7 秒。覆盖 HLS / DASH 合并成有效 MP4、音乐音轨验证、HTTP 分段、续传、取消、队列优先级、预约与限速。
- `go vet` 通过；11 个前端测试、2 个音乐桥接测试、5 个 macOS 包装测试通过；Vite 生产构建通过。
- 精简运行时执行 57 个源、24 个歌单实现和 Node v24.19.0 探测通过。公开咪咕“小星星”搜索返回 6 条可用结果，约 4.6 秒。平台注册不代表所有平台当前都可用。
- 原生 WebKit 界面通过 Computer Use 验证：页面显示正确版本与本地服务就绪；队列暂停/恢复、优先级计划保存、设置限速入口、更新包选择/校验/安装确认可操作。测试文件 1 MiB 完整下载，SHA-256 与本机生成数据一致，测试记录和文件已清理。
- 1280×800 原生窗口截图检查通过，无空白页或框架报错覆盖；玻璃界面、四角圆角与原生标题栏实现保留。未改动已由用户确认正常的 AppKit 拖动实现。

## 验证范围

桌面验收使用原生 AppKit / WKWebView 与 Computer Use；浏览器插件未提供，本轮不通过之前受限的 localhost 浏览器路径重试。原生环境没有直接采集网页开发者控制台，不能声明完整 JS 控制台检查；页面渲染、可见状态、文件字节和后端回归用于交叉验证。

iPhone 本轮只做源码架构研究，没有真机验收，也没有重新部署手机自动化。Intel 构建与 Windows 兼容性以 GitHub Actions 的对应检查为准，本机只验证 Apple 芯片安装包。
