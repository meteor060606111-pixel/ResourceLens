# 拾流 Windows 安装程序源码

1.3.0 安装程序尚未生成；本目录保留后续手动构建所需源码。

双击“拾流-1.3.0-安装程序.exe”，按简体中文向导选择位置并安装。安装后自动创建当前用户桌面快捷方式“拾流 ResourceLens”、开始菜单快捷方式及卸载入口。

默认优先安装到 D:\ResourceLens；电脑没有 D 盘时安装到当前用户的 LocalAppData\Programs\ResourceLens。可以在向导中更改位置。请选择当前用户可写入的目录，因为捕获信息及下载文件默认保存在程序目录内。

支持 Windows 10/11，兼容 x64 应用。安装包包含 FFmpeg、FFprobe、yt-dlp 与 Node.js，无需另外安装 REA、Go 或 Node.js。

安装前检查 WebView2；存在时直接使用，缺少时调用已验证微软数字签名的 Evergreen 安装器联网补装。缺少运行库的电脑需要网络连接。依赖安装失败会停止应用安装，并显示可重试的提示。

卸载只删除本安装程序安装的文件、快捷方式与卸载入口。运行后生成的下载文件和 data 文件保留，WebView2 作为共享运行库也保留。卸载前正常退出拾流，以恢复原系统代理。

视频号首次使用仍需在拾流中开始捕获、安装本机捕获证书，然后登录自己的电脑版微信并打开视频号播放。安装包不携带原电脑的账号资料、证书、任务或下载文件。

构建命令：

```powershell
.\installer\build-installer.ps1 -OutputDirectory 'D:\ResourceLens\release'
```

需 Inno Setup 6.7.3 的 ISCC.exe，可通过 -Compiler 指定。简体中文主向导及卸载界面的消息在 ResourceLens.iss 中；其余安装器底层消息使用编译器默认消息文件。

组件来源：

- Inno Setup 6.7.3：https://github.com/jrsoftware/issrc/releases/tag/is-6_7_3。安装编译器前已验证 Pyrsys B.V. 数字签名及发布哈希。
- WebView2 官方安装器：https://go.microsoft.com/fwlink/p/?LinkId=2124703。
- WebView2 检测与部署依据：https://learn.microsoft.com/microsoft-edge/webview2/concepts/distribution。
- 应用所附视频引擎及许可证见 video-engine/ORIGIN.md。
