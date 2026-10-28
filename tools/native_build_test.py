"""Native release build contracts that need no network, toolchain or build."""
import json
from pathlib import Path
import re
import tarfile
import tempfile
import unittest
import zipfile

import native_build
from native_build import (ASSETS, BUILD_ARGS, VARIANTS, BuildError, latest_release_version, parse_manifest,
                          parse_version, stamp_arguments)

ROOT = Path(__file__).resolve().parent.parent
HASHES = {f"go_{goos}_{goarch}_sha256": f"{i:064x}" for i, (goos, goarch, _) in enumerate(VARIANTS)}
MANIFEST = "\n".join(["# authored", "version=x", "go_version=1.27.1", *(f"{k}={v}" for k, v in HASHES.items()), ""])


class InstallerParity(unittest.TestCase):
    """Online rooms match players by build stamp, so the release build must
    stamp and build with exactly the source installer's arguments: Mac and
    Linux players who build from source play with Windows players who
    install these executables."""

    def test_build_arguments_match_the_source_installer(self):
        sh = (ROOT / "tools/installer/install.sh").read_text()
        sh_args = re.search(r"local build_args=\((.*?)\)", sh).group(1)
        self.assertEqual(re.findall(r"'[^']*'|\S+", sh_args), [f"'{a}'" if " " in a else a for a in BUILD_ARGS])

    def test_variants_match_the_source_installer(self):
        text = (ROOT / "tools/installer/install.sh").read_text()
        found = set(re.findall(r"(darwin|linux|windows)/(amd64|arm64)/(v1|v8\.0)=", text))
        self.assertEqual(found, set(VARIANTS))

    def test_stamp_names_every_variant_hash(self):
        args = stamp_arguments(parse_manifest(MANIFEST), "s.tar.gz", "nanolathe-abc/", "src")
        self.assertEqual(args[:len(BUILD_ARGS)], [f"-arg={a}" for a in BUILD_ARGS])
        variants = [args[i + 1] for i, a in enumerate(args) if a == "-variant"]
        self.assertEqual(len(variants), 6)
        for goos, goarch, level in VARIANTS:
            self.assertIn(f"{goos}/{goarch}/{level}={HASHES[f'go_{goos}_{goarch}_sha256']}", variants)
        self.assertEqual(args[args.index("-cgo") + 1], "0")


class Manifest(unittest.TestCase):
    def test_accepts_the_installers_format(self):
        self.assertEqual(parse_manifest(MANIFEST.replace("\n", "\r\n"))["go_version"], "1.27.1")

    def test_refuses_what_the_installers_refuse(self):
        for broken in (MANIFEST + "go_version=1.27.2\n", MANIFEST.replace("go_version=1.27.1", "go_version=1.27"),
                       MANIFEST.replace(HASHES["go_windows_arm64_sha256"], "zz"), MANIFEST + "Bad=1\n",
                       "\n".join(line for line in MANIFEST.split("\n") if not line.startswith("go_linux_amd64"))):
            with self.assertRaises(BuildError):
                parse_manifest(broken)


class Versions(unittest.TestCase):
    def test_windows_file_versions(self):
        self.assertEqual(parse_version("0.3.0.65535"), "0.3.0.65535")
        for bad in ("0.3.0", "0.3.0.65536", "0.3.0.01", "v0.3.0.1", "0.3.0.1.2"):
            with self.assertRaises(BuildError):
                parse_version(bad)

    def test_latest_release_is_the_highest_plain_version_tag(self):
        self.assertEqual(latest_release_version(["v0.2.0", "v0.10.0", "v0.3.0", "v1.0.0-rc1", "browser-380"]), "0.10.0")
        self.assertEqual(latest_release_version(["browser-380"]), "0.0.0")


class Package(unittest.TestCase):
    def test_assets_are_reproducible_and_carry_the_revision(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            build = tmp / "build"
            for goos, goarch in native_build.TARGETS:
                (build / f"{goos}-{goarch}").mkdir(parents=True)
                (build / f"{goos}-{goarch}" / native_build.executable(goos)).write_bytes(f"{goos}/{goarch}".encode())
                if goos == "windows":
                    (build / f"{goos}-{goarch}" / native_build.LAUNCHER).write_bytes(f"launcher {goarch}".encode())
            revision = "a" * 40
            (build / "build.json").write_text(json.dumps({"revision": revision}))
            sums = []
            for out in ("one", "two"):
                native_build.package_main(["--build", str(build), "--out", str(tmp / out), "--source-root", str(ROOT)])
                sums.append((tmp / out / "SHA256SUMS").read_text())
            self.assertEqual(sums[0], sums[1])
            self.assertEqual([line.split("  ")[1] for line in sums[0].splitlines()], list(ASSETS[:-1]))
            with zipfile.ZipFile(tmp / "one/nanolathe-windows-arm64.zip") as z:
                self.assertEqual(z.read("nanolathe.exe"), b"windows/arm64")
                self.assertEqual(z.read(native_build.LAUNCHER), b"launcher arm64")
                self.assertEqual(z.read("source-revision").decode().strip(), revision)
            # The download is the x64 launcher, which also runs on ARM64.
            self.assertEqual((tmp / "one" / native_build.SETUP).read_bytes(), b"launcher amd64")
            with tarfile.open(tmp / "one/nanolathe-linux-amd64.tar.gz") as t:
                self.assertEqual(t.getmember("nanolathe").mode, 0o755)
                self.assertEqual(t.extractfile("source-revision").read().decode().strip(), revision)


if __name__ == "__main__":
    unittest.main()
