"""Build a relocatable Windows music engine from an installed environment.

Run with the base Python used to create venv. Only its standard library and
runtime DLLs are copied; third-party packages come exclusively from venv.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--environment", default=str(Path(__file__).parent / "venv"))
    args = parser.parse_args()
    if sys.platform != "win32" or sys.maxsize <= 2**32:
        raise SystemExit("A 64-bit Windows Python installation is required.")
    engine = Path(__file__).resolve().parent
    output = engine / "runtime"
    if output.exists():
        raise SystemExit("runtime already exists; preserve or relocate it before rebuilding.")
    packages = Path(args.environment).resolve() / "Lib" / "site-packages"
    base = Path(sys.base_prefix).resolve()
    if not (packages / "musicdl").is_dir():
        raise SystemExit("Install musicdl 2.14.1 in the selected environment first.")
    stage = Path(tempfile.mkdtemp(prefix=".runtime-build-", dir=engine))
    try:
        names = ["python.exe", "python3.dll", "LICENSE.txt"]
        names += [p.name for p in base.glob("python3[0-9]*.dll")]
        names += [p.name for p in base.glob("vcruntime*.dll")]
        for name in names:
            shutil.copy2(base / name, stage / name)

        def stdlib_ignore(path, names):
            excluded = {"__pycache__"}
            if Path(path) == base / "Lib":
                excluded.add("site-packages")
            return set(names) & excluded

        shutil.copytree(base / "Lib", stage / "Lib", ignore=stdlib_ignore)
        shutil.copytree(base / "DLLs", stage / "DLLs", ignore=shutil.ignore_patterns("__pycache__"))
        shutil.copytree(packages, stage / "Lib" / "site-packages", ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
        dll_name = f"python{sys.version_info.major}{sys.version_info.minor}"
        (stage / (dll_name + "._pth")).write_text(".\nDLLs\nLib\nLib\\site-packages\nimport site\n", encoding="utf-8")
        # Retain the exact upstream notices in addition to each distribution's metadata.
        shutil.copy2(packages / "musicdl-2.14.1.dist-info" / "licenses" / "LICENSE", stage / "musicdl-LICENSE.txt")
        env = {key: value for key, value in os.environ.items() if not key.upper().startswith("PYTHON")}
        env["PYTHONDONTWRITEBYTECODE"] = "1"
        probe = subprocess.run([str(stage / "python.exe"), "-B", "-X", "utf8", "-c",
            "import json,sys,importlib.metadata as m,musicdl;from musicdl.modules.sources import MusicClientBuilder;"
            "print(json.dumps({'python':sys.version.split()[0],'musicdl':musicdl.__version__,"
            "'sources':len(MusicClientBuilder.REGISTERED_MODULES),'packages':sorted([(d.metadata['Name'],d.version) for d in m.distributions()])}))"],
            cwd=tempfile.gettempdir(), env=env, capture_output=True, text=True, encoding="utf-8", timeout=60, check=True)
        manifest = json.loads(probe.stdout)
        if manifest["musicdl"] != "2.14.1" or manifest["sources"] != 57:
            raise RuntimeError("Unexpected music engine version or platform registry.")
        manifest["pythonExeSha256"] = hashlib.sha256((stage / "python.exe").read_bytes()).hexdigest()
        manifest["license"] = "musicdl: PolyForm-Noncommercial-1.0.0; Python: PSF; dependency licenses retained in Lib/site-packages"
        (stage / "runtime-manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
        stage.rename(output)
        print(json.dumps({"runtime": str(output), "python": manifest["python"], "musicdl": manifest["musicdl"], "sources": manifest["sources"]}))
    finally:
        # Only this newly created staging directory is eligible for cleanup.
        if stage.exists() and stage.parent == engine and stage.name.startswith(".runtime-build-"):
            shutil.rmtree(stage)


if __name__ == "__main__":
    main()
