# macOS 构建与安装

1.8.0 完整音乐版支持 macOS 14 及以上，分别生成 Apple 芯片 (`arm64`) 和 Intel (`amd64`) 安装包。桌面使用 Wails 的原生 WKWebView，媒体与音乐引擎已内置，使用时不需要 Go、Node.js、Python 或 Homebrew。

## 从源码构建

准备 Go 1.24+、Node.js 24.19.0 / npm、Python 3 和 Xcode Command Line Tools：

```bash
xcode-select --install
./build-macos.sh
# 完整音乐引擎在对应芯片的 Mac 上原生构建；CI 自动分别构建两种架构
RESOURCELENS_ARCH=amd64 ./build-macos.sh
```

脚本核对固定 SHA-256 后下载媒体引擎、独立 CPython 和全部音乐依赖，构建前端，运行 Go 测试和 vet，再创建 `.app`、DMG 和 `.sha256`。默认产物在 `release/macos-arm64.noindex` 或 `release/macos-amd64.noindex`。`installer/macos/toolchain.json` 管理媒体工具来源与校验值；`music-toolchain.json` 固定 CPython 3.12.15、musicdl 2.14.1 及所有传递依赖。音乐环境在构建临时目录安装，通过 `pip check` 和异地运行探测后写入包中；不读取个人 venv。所有 Mach-O 动态库、扩展与子程序逐项核验架构并签名，保留原许可证。

可设置 `GO_EXECUTABLE`、`PYTHON_EXECUTABLE`、`RESOURCELENS_TOOLS_DIR`、`RESOURCELENS_MUSIC_DIR` 和 `RESOURCELENS_OUTPUT_DIR`。离线且已准备正确芯片的完整工具时使用 `RESOURCELENS_FETCH_TOOLS=0`。`RESOURCELENS_SKIP_TESTS=1` 只适用于已经完成测试的打包流程。

GitHub Actions 使用原生 Apple 芯片和 Intel runner 分别测试并构建。推送 main 后生成安装包，在两个 Mac 任务和 Windows 兼容检查通过后发布 macOS Release；Pull Request 仅验证并提供构建产物。

## 安装与数据

打开 DMG，将 `ResourceLens.app` 拖入 Applications。

- 下载：`~/Downloads/ResourceLens`，可在应用设置中修改。
- 配置、证书、日志和恢复记录：`~/Library/Application Support/ResourceLens/data`。
- 单实例锁：`~/Library/Caches/ResourceLens/instance`。
- `--root /完整路径` 可使用隔离数据目录；`--headless` 可用于本地界面测试。

开始捕获时通过系统授权修改已启用网络服务的 HTTP / HTTPS 代理，并保存原 PAC、自动发现和代理设置。停止捕获或正常退出时恢复；异常退出后下次启动读取持久恢复记录。已被用户改动的代理地址保留用户新设置。使用用户名和密码的认证代理会拒绝改写，避免丢失凭据。

HTTPS 证书安装到用户登录钥匙串，并单独设置 SSL 信任。macOS 会请求系统授权；可在设置中移除证书。没有为了测试而修改开发机器的系统代理或信任设置。

更新默认读取本项目 GitHub Releases，也支持本地 DMG 和可信 HTTPS 更新清单。校验 SHA-256、应用标识、版本、Mach-O 芯片类型与签名完整性后，在 Applications 原位交换应用目录；校验失败恢复原应用，成功后自动重新打开，并清理旧应用与暂存安装包。已有下载与配置保留。安装前需在应用中确认，进行中的下载需先暂停或完成。

## 签名与公证

默认使用 ad hoc 本地签名，未提供 Apple Developer ID 公证。首次打开请按照 DMG 中的安装说明处理系统开发者确认。

具有 Developer ID 证书和已保存公证凭据时，可以运行：

```bash
MACOS_SIGN_IDENTITY='Developer ID Application: Your Name (TEAMID)' \
MACOS_NOTARY_PROFILE='your-keychain-profile' ./build-macos.sh
```

脚本先签名独立工具及应用，再提交 DMG 公证并附加票据。无需在仓库中存储证书或凭据。

## 验证边界

自动化检查使用本地合成媒体，覆盖解析、DASH 合并、视频号前缀处理、暂停续传、兼容预览、进程取消和代理恢复。macOS 网络配置转换和恢复通过隔离快照测试；系统授权弹窗及已登录微信的真实捕获需要用户在本机完成。Mac 微信可能使用不同的请求结构或证书校验，不能沿用 README 中原 Windows 实测作为 Mac 实测结论。

## 窗口与音乐

从顶部透明的系统标题栏拖动窗口。网页使用原生 content layout guide 避开标题栏，拖动、双击及全屏行为由 AppKit 管理；没有自定义鼠标位移或坐标累加逻辑。页面标题、按钮和输入框保持普通网页交互。

音乐 Python 位于 `ResourceLens.app/Contents/Resources/music-engine/runtime/bin/python3`，不依赖当前工作目录；使用隔离解释器模式启动。已有数据目录 venv 和 `RESOURCELENS_MUSIC_PYTHON` 自定义路径仍可使用。复制或移动 `.app` 后无需修复绝对路径。打包依赖最低系统为 macOS 14，旧版 macOS 13.5 的视频工作台可从旧版本 Release 获取。


## 1.8.0 更新和副本清理

“设置 → 关于与更新”默认从 GitHub 获取本机芯片对应的稳定 macOS 安装包，自动检查可关闭，安装前会确认。只有安装在 `/Applications/ResourceLens.app` 或 `~/Applications/ResourceLens.app` 的应用支持原位升级；其他位置请先移入 Applications。

升级成功后清理旧应用与已用的暂存安装包，失败时恢复原应用；极少数恢复失败时保留恢复目录并在更新状态里给出位置。自定义下载目录、配置和用户文件不会随升级清理。安装包、主程序与已存在的 Developer ID 团队也会重新校验。

构建默认输出在 `release/macos-<arch>.noindex`，打包后不保留 `.app` 副本；需要调试应用包时显式设置 `RESOURCELENS_KEEP_APP=1`，调试结束需自行清理。CI 继续发布 DMG 与 SHA-256。原始参考仓库仅用于源码研究，没有作为应用安装。
