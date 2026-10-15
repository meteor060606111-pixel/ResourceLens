# 音乐引擎

拾流通过独立 Python 进程调用 musicdl 2.14.1。1.6.3 本机安装包包含可移动的 Windows Python 3.12 和音乐引擎，安装后无需另装 Python。应用源码仓库不纳入大型运行时；打包保留 Python 许可证、musicdl 原许可证及每个依赖的发行元数据和许可证。此本机音乐构建面向个人非商业使用，musicdl 的许可条件独立于拾流的 MIT 许可证。

源码运行：安装 Python 3.10 以上，运行本目录的 `准备音乐引擎.ps1` 创建 venv 并安装固定版本 `musicdl==2.14.1`。也可以传入 `-Repository 'D:\nixiang\musicdl'` 从已下载源码安装。已有内置 runtime 时，脚本修复该 runtime。上游仓库：https://github.com/CharlesPikachu/musicdl ，许可证标识为 PolyForm-Noncommercial-1.0.0，完整条款见运行时的 `musicdl-LICENSE.txt`。

运行时优先读取 `RESOURCELENS_MUSIC_PYTHON` 环境变量，否则查找应用或数据目录下 `music-engine/runtime/python.exe`，再查找 `music-engine/venv/Scripts/python.exe`（macOS 为 `music-engine/venv/bin/python3`）。macOS 可手动创建 venv 后安装 musicdl。Windows 打包前用创建 venv 的基础 Python 执行 `python music-engine/build_runtime.py`，从基础标准库和 venv 依赖创建独立 runtime；已存在的 runtime 不会被构建脚本覆盖。

音乐页读取引擎注册的平台列表，最多同时搜索 4 个平台；每个平台默认最多搜索 6 项。「导入歌单」只列出上游实现了歌单解析的平台，解析一个公开链接后展示前 100 项中的可用音源，支持全选或部分下载。搜索最长 150 秒，歌单最长 5 分钟，均可取消。平台是否注册不代表当前接口可用，音源受接口、账号权限和地区影响。

仅导入普通 HTTP 音频，需专用解密的受保护音源、预先处理的内容和 HLS 暂不导入。下载进入原队列，完成前检查音轨并完整解码，验证失败不发布为完成文件。实际时长明显短于平台标注时显示「可能是试听片段」。若音源返回歌词，原子保存同名 `.lrc`，不覆盖已有歌词。WMA、APE 等浏览器不兼容格式生成 AAC 播放缓存，原文件保持不变。平台请求头和 Cookie 仅保留在后端内存，重启后需要凭据的音源应重新搜索。
