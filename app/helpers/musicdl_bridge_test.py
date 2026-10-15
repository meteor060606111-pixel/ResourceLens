import importlib.util
from pathlib import Path
import sys
import tempfile
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("musicdl_bridge", Path(__file__).with_name("musicdl_bridge.py"))
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


def song(**changes):
    values = dict(with_valid_download_url=True, download_url="https://example.com/song.mp3", protocol="HTTP",
                  raw_data={}, downloaded_contents=None, identifier="123", song_name="歌曲", singers="作者",
                  album="专辑", ext="mp3", file_size_bytes=100, duration_s=8, bitrate=192, cover_url="", lyric="歌词")
    values.update(changes)
    return SimpleNamespace(**values)


class BridgeTests(unittest.TestCase):
    def test_only_unprotected_plain_http_audio_is_exported(self):
        client = SimpleNamespace(default_download_headers={"User-Agent": "test"}, default_download_cookies={"session": "private"})
        result = bridge.serialize_song(client, "FixtureMusicClient", song())
        self.assertEqual(result["headers"]["Cookie"], "session=private")
        for changes in ({"raw_data": {"play_auth": "protected"}}, {"downloaded_contents": b"processed audio"},
                        {"protocol": "HLS"}, {"download_url": "file:///C:/private"}):
            self.assertIsNone(bridge.serialize_song(client, "FixtureMusicClient", song(**changes)))
        result = bridge.serialize_song(client, "FixtureMusicClient", song(duration_s=float("nan"), bitrate="unknown"))
        self.assertEqual((result["duration"], result["bitrate"]), (0, 0))

    def test_playlist_action_invokes_parser_and_limits_results(self):
        calls = []

        class Base:
            def parseplaylist(self, **kwargs):
                raise NotImplementedError

        class Client(Base):
            def search(self, **kwargs):
                raise AssertionError("Playlist was routed to search")

            def parseplaylist(self, **kwargs):
                calls.append(kwargs)
                return [song(identifier=str(i)) for i in range(105)]

        musicdl = ModuleType("musicdl")
        musicdl.__version__ = "fixture"
        sources = ModuleType("musicdl.modules.sources")
        sources.MusicClientBuilder = SimpleNamespace(REGISTERED_MODULES={"FixtureMusicClient": Client})
        sources.BuildMusicClient = lambda config: Client()
        base = ModuleType("musicdl.modules.sources.base")
        base.BaseMusicClient = Base
        with patch.dict(sys.modules, {"musicdl": musicdl, "musicdl.modules.sources": sources, "musicdl.modules.sources.base": base}):
            self.assertTrue(bridge.perform({"action": "probe"})["sources"][0]["playlist"])
            with tempfile.TemporaryDirectory() as work:
                result = bridge.perform({"action": "playlist", "url": "https://example.com/list", "sources": ["FixtureMusicClient"], "workDir": work})
        self.assertEqual(len(result["tracks"]), 100)
        self.assertEqual(calls[0]["playlist_url"], "https://example.com/list")
        self.assertIn("105", result["results"][0]["note"])
        self.assertEqual(calls[0]["request_overrides"]["timeout"], (5, 12))


if __name__ == "__main__":
    unittest.main()
