# 视频引擎来源

以下工具由 ResourceLens 作为独立进程调用，随便携包保存在同一目录。

- yt-dlp 2026.08.19 Windows x64： https://github.com/yt-dlp/yt-dlp/releases/tag/2026.08.19
  - yt-dlp.exe SHA-256：66674953fe251b89f4d08c5f0e35e0728679bd67ab3d7d05c0562af101dd3e7a
  - 源码：https://github.com/yt-dlp/yt-dlp/tree/2026.08.19
  - 项目自身采用 Unlicense；单文件构建所包含的依赖各自许可证见 yt-dlp-THIRD_PARTY_LICENSES.txt。
- FFmpeg / FFprobe 9.0.2 essentials Windows x64： https://www.gyan.dev/ffmpeg/builds/
  - ZIP 使用发布方 .sha256 校验后提取。
  - GPL v3 完整许可证：ffmpeg-LICENSE。
  - 构建信息及关联第三方组件：ffmpeg-README.txt。
  - 构建方标注源码版本：https://github.com/FFmpeg/FFmpeg/commit/946fcce07b
- Node.js 24.19.0 Windows x64： https://nodejs.org/dist/v24.19.0/
  - 使用已校验的本机便携运行时；许可证和内含第三方组件授权见 node-LICENSE.txt。

应用不会静默下载额外脚本或解析器。更新视频引擎时，应从上述项目的官方发布渠道获取并核对发布哈希，保持三个媒体工具完整。
