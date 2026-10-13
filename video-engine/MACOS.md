# macOS 媒体引擎

macOS 应用通过独立进程调用以下工具；二进制位于 `.app/Contents/Resources/video-engine`，不提交到 Git。安装包包含许可证、版本信息和完整下载哈希清单。

- yt-dlp 2026.08.19：上游的 `yt-dlp_macos` 通用二进制。来源及源码：https://github.com/yt-dlp/yt-dlp/releases/tag/2026.08.19 。使用上游 SHA2-256SUMS 核对。
- Node.js 24.19.0：上游 macOS arm64 / x64 归档。来源及对应源码：https://nodejs.org/dist/v24.19.0/ 。使用上游 SHASUMS256.txt 核对；从各归档提取原始 LICENSE。
- FFmpeg / FFprobe：OSXExperts 静态构建，Apple 芯片 9.0、Intel 8.0，GPL。构建方：https://www.osxexperts.net/ ，许可说明：https://www.osxexperts.net/license.html 。FFmpeg 源码：https://github.com/FFmpeg/FFmpeg/tree/n9.0 和 https://github.com/FFmpeg/FFmpeg/tree/n8.0 。完整构建参数随安装包记录在 ffmpeg-build.txt；第三方库及相应源码以构建方的源码和许可说明为准。

`installer/macos/toolchain.json` 固定下载地址及 SHA-256。FFmpeg 归档校验值是本次取得的静态归档哈希；构建脚本会拒绝内容改变的归档，不使用未固定版本的下载结果。所有工具仅依赖 macOS 自带的系统库，不依赖 Homebrew。

源码构建可运行 `python3 installer/macos/prepare-tools.py --arch arm64` 或 `--arch amd64`。更新工具时需重新核对来源、版本、许可证和哈希；应用运行时不会自行下载工具。
