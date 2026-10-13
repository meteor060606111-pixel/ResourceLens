#!/usr/bin/env python3
"""Fetch pinned standalone macOS tools and check every archive's SHA-256."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import tarfile
import tempfile
import urllib.request
import zipfile

REPO = Path(__file__).resolve().parents[2]


def digest(path):
    h = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def prepare(arch, output):
    manifest = json.loads((Path(__file__).parent / "toolchain.json").read_text())
    cache = REPO / "release" / "tool-cache"
    cache.mkdir(parents=True, exist_ok=True)
    output.mkdir(parents=True, exist_ok=True)
    for name, item in manifest["architectures"][arch].items():
        archive = cache / item["sha256"]
        if not archive.exists() or digest(archive) != item["sha256"]:
            print(f"Downloading {name} ({arch})", flush=True)
            request = urllib.request.Request(item["url"], headers={"User-Agent": "ResourceLens-build"})
            with tempfile.NamedTemporaryFile(dir=cache, delete=False) as part:
                temp = Path(part.name)
                try:
                    with urllib.request.urlopen(request, timeout=120) as response:
                        shutil.copyfileobj(response, part)
                    part.flush()
                    if digest(temp) != item["sha256"]:
                        raise RuntimeError(f"SHA-256 mismatch for {name}; download rejected")
                    temp.replace(archive)
                finally:
                    temp.unlink(missing_ok=True)
        if item["kind"] == "binary":
            shutil.copyfile(archive, output / name)
        elif item["kind"] == "zip":
            with zipfile.ZipFile(archive) as source:
                (output / name).write_bytes(source.read(item["member"]))
        else:
            with tarfile.open(archive) as source:
                (output / name).write_bytes(source.extractfile(item["member"]).read())
                if item.get("licenseMember"):
                    (output / "node-LICENSE.txt").write_bytes(source.extractfile(item["licenseMember"]).read())
        (output / name).chmod(0o755)
    for name in ["yt-dlp-LICENSE.txt", "yt-dlp-THIRD_PARTY_LICENSES.txt", "ffmpeg-LICENSE", "ORIGIN.md", "MACOS.md"]:
        shutil.copyfile(REPO / "video-engine" / name, output / name) if (REPO / "video-engine" / name).resolve() != (output / name).resolve() else None
    shutil.copyfile(Path(__file__).parent / "toolchain.json", output / "toolchain.json")
    print(f"Verified tools ready: {output}", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--arch", choices=["arm64", "amd64"], required=True)
    parser.add_argument("--output", type=Path, default=REPO / "video-engine")
    args = parser.parse_args()
    prepare(args.arch, args.output.resolve())
