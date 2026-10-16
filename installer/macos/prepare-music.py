#!/usr/bin/env python3
"""Build the pinned, relocatable macOS music runtime without using system Python packages."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request

REPO = Path(__file__).resolve().parents[2]
MANIFEST = Path(__file__).with_name("music-toolchain.json")
MACHO_MAGICS = {bytes.fromhex(s) for s in ["cffaedfe", "cefaedfe", "feedfacf", "feedface", "cafebabe", "bebafeca", "cafebabf", "bfbafeca"]}


def digest(path):
    h = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def fetch(item, cache):
    destination = cache / item["sha256"] / item["filename"]
    if destination.exists() and digest(destination) == item["sha256"]:
        return destination
    destination.parent.mkdir(parents=True, exist_ok=True)
    request = urllib.request.Request(item["url"], headers={"User-Agent": "ResourceLens-build"})
    with tempfile.NamedTemporaryFile(dir=destination.parent, delete=False) as output:
        part = Path(output.name)
        try:
            with urllib.request.urlopen(request, timeout=180) as response:
                shutil.copyfileobj(response, output)
            output.flush()
            if digest(part) != item["sha256"]:
                raise RuntimeError("SHA-256 mismatch: " + item["filename"])
            part.replace(destination)
        finally:
            part.unlink(missing_ok=True)
    return destination


def extract_python(archive, destination):
    with tarfile.open(archive) as source:
        for item in source.getmembers():
            name = Path(item.name)
            if name.is_absolute() or ".." in name.parts or not name.parts or name.parts[0] != "python":
                raise ValueError("Unsafe Python archive member")
            if item.isdev() or item.isfifo():
                raise ValueError("Unsupported Python archive member")
            if item.issym() or item.islnk():
                target = (destination / (name.parent if item.issym() else Path()) / item.linkname).resolve()
                if not target.is_relative_to(destination.resolve()):
                    raise ValueError("Python archive link escapes staging directory")
        source.extractall(destination)


def native_files(root):
    for path in sorted(root.rglob("*")):
        if not path.is_file() or path.is_symlink():
            continue
        with path.open("rb") as source:
            if source.read(4) in MACHO_MAGICS:
                yield path


def python_run(python, args, cwd):
    environment = {k: v for k, v in os.environ.items() if not k.startswith("PYTHON")}
    environment["PIP_DISABLE_PIP_VERSION_CHECK"] = "1"
    environment["PIP_NO_INPUT"] = "1"
    return subprocess.run([str(python), "-I", "-B", "-X", "utf8"] + args, cwd=cwd,
                          env=environment, check=True, capture_output=True, text=True, timeout=180)


def probe(runtime, work):
    result = python_run(runtime / "bin" / "python3", ["-c", (
        "import json,sys,musicdl,importlib.metadata as m;"
        "from musicdl.modules.sources import MusicClientBuilder;"
        "print(json.dumps({'python':sys.version.split()[0],'musicdl':musicdl.__version__,"
        "'sources':len(MusicClientBuilder.REGISTERED_MODULES),"
        "'packages':sorted([(d.metadata['Name'],d.version) for d in m.distributions()])}))"
    )], work)
    data = json.loads(result.stdout)
    if data["musicdl"] != "2.14.1" or data["sources"] != 57:
        raise RuntimeError("Unexpected bundled music engine")
    return data


def prepare(arch, output):
    manifest = json.loads(MANIFEST.read_text())
    fingerprint = hashlib.sha256(MANIFEST.read_bytes() + Path(__file__).read_bytes()).hexdigest()
    state_file = output / "runtime-manifest.json"
    previous = json.loads(state_file.read_text()) if state_file.exists() else {}
    if previous.get("buildFingerprint") == fingerprint:
        with tempfile.TemporaryDirectory(prefix="resourcelens-music-probe-") as work:
            probe(output / "runtime", work)
        print("Verified existing music runtime: " + str(output), flush=True)
        return
    if output.exists() and (previous.get("musicdl") != "2.14.1" or previous.get("arch") != arch):
        raise RuntimeError("Preserve or relocate the existing music build before rebuilding: " + str(output))
    output.parent.mkdir(parents=True, exist_ok=True)
    cache = REPO / "release" / "music-tool-cache"
    cache.mkdir(parents=True, exist_ok=True)
    items = manifest["architectures"][arch]
    archive = fetch(items["python"], cache)
    with ThreadPoolExecutor(max_workers=6) as pool:
        wheels = list(pool.map(lambda item: fetch(item, cache), items["wheels"]))
    source_package = fetch(manifest["constructSource"], cache)
    build_wheels = [fetch(item, cache) for item in manifest["buildWheels"]]
    with tempfile.TemporaryDirectory(prefix=".music-build-", dir=output.parent) as tmp:
        stage = Path(tmp)
        extract_python(archive, stage)
        runtime = stage / "python"
        python = runtime / "bin" / "python3"
        subprocess.run(["lipo", str(python), "-verify_arch", "arm64" if arch == "arm64" else "x86_64"], check=True)
        common = ["-m", "pip", "install", "--no-index", "--no-deps", "--no-compile", "--no-cache-dir"]
        python_run(python, common + [str(p) for p in build_wheels], stage)
        python_run(python, common + ["--no-build-isolation", str(source_package)], stage)
        python_run(python, common + [str(p) for p in wheels], stage)
        python_run(python, ["-m", "pip", "check"], stage)
        data = probe(runtime, stage)
        packages = runtime / "lib" / "python3.12" / "site-packages"
        license_path = packages / "musicdl-2.14.1.dist-info" / "licenses" / "LICENSE"
        shutil.copy2(license_path, runtime / "musicdl-LICENSE.txt")
        # Generated console entry points contain build-directory shebangs. The
        # application only launches the interpreter; keep no broken shortcuts.
        for entry in (runtime / "bin").iterdir():
            if not entry.name.startswith("python") and (entry.is_file() or entry.is_symlink()):
                entry.unlink()
        destination = stage / "engine"
        destination.mkdir()
        runtime.rename(destination / "runtime")
        data.update({"arch": arch, "buildFingerprint": fingerprint,
                     "toolchainSha256": hashlib.sha256(MANIFEST.read_bytes()).hexdigest(), "minimumMacOS": "14.0",
                     "pythonArchiveSha256": items["python"]["sha256"],
                     "license": "musicdl: PolyForm-Noncommercial-1.0.0; Python: PSF; dependency licenses retained in dist-info"})
        (destination / "runtime-manifest.json").write_text(json.dumps(data, ensure_ascii=False, indent=2))
        shutil.copy2(REPO / "music-engine" / "README.md", destination / "README.md")
        with tempfile.TemporaryDirectory(prefix="resourcelens-music-relocation-") as work:
            probe(destination / "runtime", work)
        backup = stage / "previous-runtime"
        if output.exists():
            output.rename(backup)
        try:
            destination.rename(output)
        except OSError:
            if backup.exists():
                backup.rename(output)
            raise
    print("Prepared portable music engine: " + str(output), flush=True)


def sign(root, identity, arch=None):
    for path in native_files(root):
        if arch:
            subprocess.run(["lipo", str(path), "-verify_arch", "arm64" if arch == "arm64" else "x86_64"], check=True)
        command = ["codesign", "--force", "--sign", identity]
        if identity != "-":
            command += ["--timestamp", "--options", "runtime", "--entitlements", str(Path(__file__).with_name("entitlements.plist"))]
        subprocess.run(command + [str(path)], check=True)
        subprocess.run(["codesign", "--verify", "--strict", str(path)], check=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--arch", choices=["arm64", "amd64"])
    parser.add_argument("--output", type=Path)
    parser.add_argument("--sign", type=Path)
    parser.add_argument("--identity", default="-")
    args = parser.parse_args()
    if args.sign:
        sign(args.sign.resolve(), args.identity, args.arch)
    elif args.arch and args.output:
        prepare(args.arch, args.output.resolve())
    else:
        parser.error("Specify --arch with --output, or --sign")
