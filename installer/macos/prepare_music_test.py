import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('prepare_music', Path(__file__).with_name('prepare-music.py'))
prepare_music = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare_music)


class PortableMusicBuildTests(unittest.TestCase):
    def test_python_archive_cannot_write_outside_stage(self):
        for name, target in [('python/../../escape', None), ('python/bin/escape', '/tmp/escape')]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as root:
                archive = Path(root) / 'bad.tar'
                with tarfile.open(archive, 'w') as tar:
                    info = tarfile.TarInfo(name)
                    if target:
                        info.type = tarfile.SYMTYPE
                        info.linkname = target
                        tar.addfile(info)
                    else:
                        info.size = 1
                        tar.addfile(info, io.BytesIO(b'x'))
                stage = Path(root) / 'stage'
                stage.mkdir()
                with self.assertRaises(ValueError):
                    prepare_music.extract_python(archive, stage)
                self.assertEqual(list(stage.iterdir()), [])

    def test_cached_artifact_is_verified_and_keeps_its_original_filename(self):
        with tempfile.TemporaryDirectory() as root:
            source = Path(root) / 'input.whl'
            source.write_bytes(b'wheel fixture')
            item = {'filename': 'package.whl', 'url': source.as_uri(), 'sha256': hashlib.sha256(source.read_bytes()).hexdigest()}
            cache = Path(root) / 'cache'
            cached = prepare_music.fetch(item, cache)
            self.assertEqual(cached.name, 'package.whl')
            cached.write_bytes(b'corrupt')
            self.assertEqual(prepare_music.fetch(item, cache).read_bytes(), source.read_bytes())
            item['sha256'] = '0' * 64
            with self.assertRaises(RuntimeError):
                prepare_music.fetch(item, cache)

    def test_both_architectures_pin_the_same_engine_and_packages(self):
        manifest = json.loads(prepare_music.MANIFEST.read_text())
        versions = []
        for arch in ['arm64', 'amd64']:
            runtime = manifest['architectures'][arch]
            self.assertIn('apple-darwin', runtime['python']['filename'])
            self.assertEqual(len(runtime['python']['sha256']), 64)
            packages = {i['name'].lower().replace('_', '-'): i['version'] for i in runtime['wheels']}
            self.assertEqual(packages['musicdl'], '2.14.1')
            self.assertEqual(len(packages), 51)
            self.assertTrue(all(i['url'].startswith('https://files.pythonhosted.org/') and len(i['sha256']) == 64 for i in runtime['wheels']))
            versions.append(packages)
        self.assertEqual(versions[0], versions[1])
        self.assertEqual(manifest['constructSource']['version'], '2.8.8')


if __name__ == '__main__':
    unittest.main()
