拾流现在支持 macOS 13.5 及以上，提供两个独立安装包：

- `macos-arm64.dmg`：Apple 芯片（M 系列）。
- `macos-amd64.dmg`：Intel Mac。

打开 DMG，将 ResourceLens.app 拖入 Applications。内置 yt-dlp、FFmpeg、FFprobe 和 Node.js，无需另装媒体工具。保留资源捕获、画质选择、下载队列、暂停续传和预览工作台；配置及日志位于 Application Support，下载默认位于 Downloads/ResourceLens。

首次 HTTPS 捕获需要 macOS 钥匙串和网络设置授权。停止捕获或正常退出恢复原代理；异常退出后下次启动恢复。真实微信视频号捕获取决于 Mac 客户端请求及证书校验，自动化测试使用合成媒体，并未宣称已登录 Mac 微信的完整实测。

当前安装包使用 ad hoc 本地签名，未做 Apple Developer ID 公证。若系统提示无法验证开发者，请按照 DMG 内的安装说明，通过右键“打开”或“系统设置 → 隐私与安全性 → 仍要打开”确认。

更新可在设置中核验并打开新版 DMG，退出后拖拽替换应用。配置、历史和已下载文件保留。文件校验值见同名 `.sha256`；Windows 现有安装版仍可从 v1.3.0 获取。
