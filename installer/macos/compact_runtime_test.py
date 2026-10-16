import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('compact_runtime', Path(__file__).with_name('compact-runtime.py'))
compact_runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(compact_runtime)


class RuntimeCompactionTests(unittest.TestCase):
    def test_shared_node_remains_valid_after_bundle_relocation(self):
        with tempfile.TemporaryDirectory() as directory:
            bundle = Path(directory) / 'ResourceLens.app'
            resources = bundle / 'Contents' / 'Resources'
            node = resources / 'video-engine' / 'node'
            node.parent.mkdir(parents=True)
            node.write_bytes(b'shared runtime')
            wrapper = resources / 'music-engine/runtime/lib/python3.12/site-packages/nodejs_wheel'
            (wrapper / 'bin').mkdir(parents=True)
            (wrapper / 'bin/node').write_bytes(b'duplicate runtime')
            (wrapper / 'executable.py').write_text('wrapper API')
            (wrapper / 'include').mkdir()
            (wrapper / 'include/dev.h').write_text('headers')
            compact_runtime.share_node(bundle)
            link = wrapper / 'bin/node'
            self.assertTrue(link.is_symlink())
            self.assertFalse(Path(link.readlink()).is_absolute())
            moved = Path(directory) / 'relocated app.app'
            bundle.rename(moved)
            moved_link = moved / link.relative_to(bundle)
            self.assertEqual(moved_link.read_bytes(), b'shared runtime')
            self.assertTrue((moved / wrapper.relative_to(bundle) / 'executable.py').exists())

    def test_pruning_preserves_runtime_data_and_licenses(self):
        with tempfile.TemporaryDirectory() as directory:
            runtime = Path(directory)
            paths = ['include/dev.h', 'lib/python3.12/ensurepip/tool.py',
                     'lib/python3.12/site-packages/pip/__init__.py',
                     'lib/python3.12/site-packages/musicdl/__init__.py',
                     'lib/python3.12/site-packages/musicdl/data.json',
                     'lib/python3.12/site-packages/musicdl-2.14.1.dist-info/licenses/LICENSE',
                     'musicdl-LICENSE.txt']
            for name in paths:
                path = runtime / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text('preserve or remove fixture')
            compact_runtime.prune_development_files(runtime)
            for name in paths[:3]:
                self.assertFalse((runtime / name).exists())
            for name in paths[3:]:
                self.assertTrue((runtime / name).exists())


if __name__ == '__main__':
    unittest.main()
