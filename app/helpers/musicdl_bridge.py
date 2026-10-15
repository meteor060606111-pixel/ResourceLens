"""JSON bridge to an independently installed musicdl 2.x engine.

No platform code is copied into ResourceLens. stdout is exclusively JSON;
musicdl diagnostics are suppressed so source cookies never enter UI logs.
"""
import contextlib
import json
import math
import os
import sys
import warnings
from concurrent.futures import ThreadPoolExecutor

LABELS = {
    "MiguMusicClient": "咪咕音乐", "NeteaseMusicClient": "网易云音乐",
    "QQMusicClient": "QQ 音乐", "KuwoMusicClient": "酷我音乐",
    "KugouMusicClient": "酷狗音乐", "QianqianMusicClient": "千千音乐",
    "SodaMusicClient": "汽水音乐", "BilibiliMusicClient": "哔哩哔哩",
    "WikimediaCommonsMusicClient": "维基共享音频", "JamendoMusicClient": "Jamendo",
    "FMAMusicClient": "Free Music Archive", "StreetVoiceMusicClient": "街声",
    "XimalayaMusicClient": "喜马拉雅", "LizhiMusicClient": "荔枝 FM",
    "QingtingMusicClient": "蜻蜓 FM", "LRTSMusicClient": "懒人听书",
    "BodianMusicClient": "波点音乐", "FiveSingMusicClient": "5SING",
    "ITunesMusicClient": "Apple 播客", "TIDALMusicClient": "TIDAL",
}
FAVORITES = ["MiguMusicClient", "NeteaseMusicClient", "QQMusicClient", "KuwoMusicClient",
             "KugouMusicClient", "QianqianMusicClient", "WikimediaCommonsMusicClient", "JamendoMusicClient"]


def label(source):
    return LABELS.get(source, source.removesuffix("MusicClient"))


def text(value, limit=500):
    return str(value or "")[:limit]


def number(value):
    try:
        value = float(value or 0)
        return max(0, value) if math.isfinite(value) else 0
    except (TypeError, ValueError):
        return 0


def serialize_song(client, source, song):
    url = getattr(song, "download_url", None)
    raw = getattr(song, "raw_data", {}) or {}
    if (not song.with_valid_download_url or not isinstance(url, str)
            or not url.startswith(("https://", "http://"))
            or text(getattr(song, "protocol", "HTTP")).upper() != "HTTP"
            or raw.get("play_auth") or getattr(song, "downloaded_contents", None)):
        return None
    headers = dict(getattr(client, "default_download_headers", {}) or {})
    headers.update(getattr(song, "default_download_headers", {}) or {})
    cookies = dict(getattr(client, "default_download_cookies", {}) or {})
    cookies.update(getattr(song, "default_download_cookies", {}) or {})
    if cookies:
        headers["Cookie"] = "; ".join(f"{k}={v}" for k, v in cookies.items())
    return {"source": source, "sourceLabel": label(source), "identifier": text(song.identifier),
        "title": text(song.song_name, 200), "artist": text(song.singers, 200), "album": text(song.album, 200),
        "ext": text(song.ext, 12).lstrip(".").lower(), "url": url, "size": int(number(song.file_size_bytes)),
        "duration": number(song.duration_s), "bitrate": number(song.bitrate),
        "cover": text(song.cover_url, 4000), "lyric": text(song.lyric, 20000),
        "headers": {str(k): str(v) for k, v in headers.items() if "\n" not in str(k) + str(v) and "\r" not in str(k) + str(v)}}


def perform(request):
    import musicdl
    from musicdl.modules.sources import BuildMusicClient, MusicClientBuilder
    from musicdl.modules.sources.base import BaseMusicClient
    sources = MusicClientBuilder.REGISTERED_MODULES
    if request.get("action") == "probe":
        ordered = [s for s in FAVORITES if s in sources] + sorted(s for s in sources if s not in FAVORITES)
        return {"ready": True, "version": musicdl.__version__,
                "sources": [{"id": s, "label": label(s), "playlist": sources[s].parseplaylist is not BaseMusicClient.parseplaylist} for s in ordered]}
    action = request.get("action")
    if action not in {"search", "playlist"}:
        raise ValueError("Unsupported music operation")
    keyword = text(request.get("keyword"), 121).strip()
    selected = list(dict.fromkeys(request.get("sources") or []))
    if (action == "search" and (not keyword or len(keyword) > 120)) or not 1 <= len(selected) <= 4 or any(s not in sources for s in selected):
        raise ValueError("请输入 1–120 字的关键词并选择有效平台")
    if action == "playlist" and (len(selected) != 1 or not text(request.get("url"), 4097).startswith(("https://", "http://"))):
        raise ValueError("请选择歌单平台并输入歌单链接")
    work = request["workDir"]

    def search(source):
        tracks = []
        skipped = 0
        try:
            client = BuildMusicClient({"type": source, "disable_print": True, "work_dir": os.path.join(work, source),
                "search_size_per_source": 6, "search_size_per_page": 6, "max_retries": 1})
            if action == "playlist":
                songs = client.parseplaylist(playlist_url=request["url"], request_overrides={"timeout": (5, 12)}) or []
            else:
                songs = client.search(keyword=keyword, num_threadings=2, request_overrides={"timeout": (5, 12)}) or []
            limit = 100 if action == "playlist" else 12
            for song in songs[:limit]:
                track = serialize_song(client, source, song)
                if track is None:
                    skipped += 1
                    continue
                tracks.append(track)
            notes = []
            if skipped:
                notes.append(f"{skipped} 项音源需专用处理，暂不支持导入")
            if len(songs) > limit:
                notes.append(f"歌单共返回 {len(songs)} 项，本次展示前 {limit} 项中的可用音源")
            note = "；".join(notes)
            return tracks, {"source": source, "label": label(source), "count": len(tracks), "note": note}
        except Exception:
            return [], {"source": source, "label": label(source), "count": 0, "note": "该平台暂未返回可用音源，请稍后重试或切换平台"}

    with ThreadPoolExecutor(max_workers=len(selected)) as pool:
        results = list(pool.map(search, selected))
    return {"tracks": [song for songs, _ in results for song in songs], "results": [result for _, result in results]}


def main():
    warnings.filterwarnings("ignore")
    try:
        request = json.load(sys.stdin)
        with open(os.devnull, "w", encoding="utf-8") as quiet, contextlib.redirect_stdout(quiet), contextlib.redirect_stderr(quiet):
            response = perform(request)
        response["ok"] = True
    except Exception as error:
        response = {"ok": False, "error": "音乐引擎启动失败，请运行 music-engine 中的准备脚本后重试" if isinstance(error, ImportError) else "音乐引擎处理失败，请检查平台选项后重试"}
    sys.stdout.write(json.dumps(response, ensure_ascii=True, allow_nan=False))


if __name__ == "__main__":
    main()
