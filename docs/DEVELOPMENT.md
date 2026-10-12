# 开发目录说明

Go 模块定义在仓库根目录，程序入口与后端位于 app，Vue 界面位于 frontend。frontend/assets.go 将 Vite 的 dist 产物提供给桌面窗口和本地浏览器模式。

## 后端文件导航

| 范围 | 主要文件 |
| --- | --- |
| 启动、API 和本机状态 | main.go、engine.go、session.go |
| 通用下载、暂停续传 | download.go、transfer.go、video_transfer.go |
| 平台与画质解析 | video.go、douyin.go、platforms.go、wechat.go、wechat_quality.go |
| 预览、批量整理 | media.go、features.go |
| Windows 代理与证书 | proxy.go、certificate.go、system_windows.go |
| 自检、升级、单实例 | maintenance.go、update.go、instance_windows.go |

各范围的 *_test.go 与对应实现保持在同一 Go 包，嵌入的浏览器和视频号观察脚本放在 app/helpers。

## 本机构建

按根目录 README 准备 Node.js、Go 和 video-engine 工具，然后运行：

```powershell
.\build.ps1
```

需要单独检查后端时，先构建界面：

```powershell
npm ci --prefix frontend --ignore-scripts
npm run build --prefix frontend
go test -tags 'desktop,production' ./app ./frontend
```

构建后的 ResourceLens.exe 位于项目根目录，继续从旁边的 data、downloads 和 video-engine 读取本机运行文件。目录整理不移动下载视频或本机配置。安装程序在后续单独构建。
