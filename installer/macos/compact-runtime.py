#!/usr/bin/env python3
"""Compact a staging app, retaining runtime dependencies and third-party licenses.

This never modifies the user's installed application or the reusable build cache.
Node is shared by the video and music engines through a relocatable bundle link.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess

spec = importlib.util.spec_from_file_location("prepare_music", Path(__file__).with_name("prepare-music.py"))
prepare_music = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare_music)


def size(root):
    return sum(path.stat().st_size for path in root.rglob("*") if path.is_file() and not path.is_symlink())


def prune_development_files(runtime):
    # Keep .dist-info, license files and all application modules/data. No
    # on-device package installation, native compilation or Tk UI is used.
    library = runtime / "lib" / "python3.12"
    paths = [runtime / "include", runtime / "share" / "man",
             library / "ensurepip", library / "idlelib", library / "tkinter",
             library / "turtledemo", library / "turtle.py", library / "test",
             library / "site-packages" / "pip"]
    paths += list((runtime / "bin").glob("*config"))
    paths += list((runtime / "lib").glob("*tcl*"))
    paths += list((runtime / "lib").glob("*tk*"))
    paths += list((runtime / "lib").glob("itcl*"))
    paths += list((runtime / "lib").glob("thread*"))
    paths += list((library / "lib-dynload").glob("_tkinter*"))
    paths += list((library / "lib-dynload").glob("_test*"))
    paths += sorted(runtime.rglob("__pycache__"), key=lambda p: len(p.parts), reverse=True)
    for path in paths:
        if path.is_symlink() or path.is_file():
            path.unlink()
        elif path.is_dir():
            shutil.rmtree(path)


def share_node(bundle):
    resources = bundle / "Contents" / "Resources"
    shared = resources / "video-engine" / "node"
    runtime = resources / "music-engine" / "runtime"
    wheel = runtime / "lib" / "python3.12" / "site-packages" / "nodejs_wheel"
    if not shared.is_file() or not wheel.is_dir():
        raise RuntimeError("Expected bundled video Node and music Node wrapper")
    # npm/corepack, development headers and manual pages are not used by the
    # downloader. Keep Python API and distribution metadata/licenses intact.
    for name in ["bin", "include", "lib", "share"]:
        path = wheel / name
        if path.is_dir():
            shutil.rmtree(path)
    (wheel / "bin").mkdir()
    target = wheel / "bin" / "node"
    target.symlink_to(os.path.relpath(shared, target.parent))
    if target.resolve() != shared.resolve():
        raise RuntimeError("Shared Node link escaped the app resources")


def compact(bundle, strip_symbols=True):
    resources = bundle / "Contents" / "Resources"
    runtime = resources / "music-engine" / "runtime"
    before = size(bundle)
    share_node(bundle)
    shared_saving = before - size(bundle)
    before_trim = size(bundle)
    prune_development_files(runtime)
    # Standalone CPython is statically linked. Remove its embedding library only
    # after checking every shipped Mach-O for a dynamic dependency on it.
    python_library = runtime / "lib" / "libpython3.12.dylib"
    binaries = list(prepare_music.native_files(runtime))
    if python_library.exists():
        needed = False
        for binary in binaries:
            if binary == python_library:
                continue
            output = subprocess.check_output(["otool", "-L", str(binary)], text=True)
            if "libpython3.12" in output:
                needed = True
                break
        if not needed:
            python_library.unlink()
    trimmed_saving = before_trim - size(bundle)
    before_strip = size(bundle)
    if strip_symbols:
        for binary in prepare_music.native_files(resources):
            # PyInstaller appends its Python archive after Mach-O segments;
            # strip would silently discard it even though the Mach-O stays valid.
            if binary.name == "yt-dlp":
                continue
            subprocess.run(["strip", "-S", "-x", str(binary)], check=True, capture_output=True)
    after = size(bundle)
    report = {"beforeBytes": before, "afterBytes": after, "savedBytes": before-after,
              "sharedNodeSavedBytes": shared_saving, "developmentFilesSavedBytes": trimmed_saving,
              "symbolsSavedBytes": before_strip-after, "sharedNode": "video-engine/node",
              "licensesRetained": True}
    (resources / "runtime-footprint.json").write_text(json.dumps(report, indent=2))
    return report


def verify(bundle):
    resources = bundle / "Contents" / "Resources"
    runtime = resources / "music-engine" / "runtime"
    node = resources / "video-engine" / "node"
    expected = subprocess.check_output([str(node), "--version"], text=True).strip()
    for tool in ["yt-dlp", "ffmpeg", "ffprobe"]:
        flag = "--version" if tool == "yt-dlp" else "-version"
        subprocess.run([str(resources / "video-engine" / tool), flag], check=True,
                       capture_output=True, timeout=45)
    script = ("import json,subprocess,nodejs_wheel,musicdl;"
              "from musicdl.modules.sources import MusicClientBuilder;"
              "from musicdl.modules.sources.base import BaseMusicClient;"
              "result=nodejs_wheel.node(['--version'],return_completed_process=True,capture_output=True,text=True);"
              "result.check_returncode();"
              "print(json.dumps({'musicdl':musicdl.__version__,'sources':len(MusicClientBuilder.REGISTERED_MODULES),"
              "'playlists':sum(c.parseplaylist is not BaseMusicClient.parseplaylist for c in MusicClientBuilder.REGISTERED_MODULES.values()),"
              "'node':result.stdout.strip()}))")
    output = subprocess.check_output([str(runtime / "bin" / "python3"), "-I", "-B", "-c", script], text=True)
    result = json.loads(output)
    if result != {"musicdl": "2.14.1", "sources": 57, "playlists": 24, "node": expected}:
        raise RuntimeError("Compacted runtime failed functional probe: " + output)
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--verify", action="store_true")
    args = parser.parse_args()
    bundle = args.bundle.resolve()
    if args.verify:
        print(json.dumps(verify(bundle)))
    else:
        print(json.dumps(compact(bundle)))
