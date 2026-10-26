"""Build and package Nanolathe's prebuilt native release executables.

The build reproduces the source installers (tools/installer/install.sh and
install.ps1) exactly, because online rooms admit only stamped builds and two
players match only when their stamps do (docs/DESIGN_MULTIPLAYER.md §8.7):
it downloads the Go release that https://nanolathe.gg/install/release.txt
pins and verifies its archive hash, downloads the commit's archive from
codeload, stamps it with stampgen using the installers' arguments and all
six admitted toolchain hashes, and builds with the installers' flags. Go
builds the same executable on any host, so one Linux job cross-compiles
every target.

After stamping, each Windows target gets a version resource and icon
(internal/version/winresgen), which code signing requires. The resource is
not part of the stamped source, so the stamp stays the same. Windows targets
also build the launcher (cmd/nanolathe-launcher), with its own resource.

`build` writes one directory per target, holding only its executables, so
the Windows executables can go to signing as they are, and build.json.
`package` turns that directory into the release assets and SHA256SUMS:
per-platform archives, and Nanolathe-Setup.exe, the x64 launcher a Windows
player downloads first.
"""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.parse
import urllib.request
import zipfile

MANIFEST_URL = "https://nanolathe.gg/install/release.txt"

# The installers' build arguments and the stamp's admitted toolchains.
BUILD_ARGS = ("-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "./cmd/nanolathe")
VARIANTS = (("darwin", "amd64", "v1"), ("darwin", "arm64", "v8.0"), ("linux", "amd64", "v1"),
            ("linux", "arm64", "v8.0"), ("windows", "amd64", "v1"), ("windows", "arm64", "v8.0"))
TARGETS = (("windows", "amd64"), ("windows", "arm64"), ("linux", "amd64"), ("linux", "arm64"))
LEVELS = {"amd64": ("GOAMD64", "v1"), "arm64": ("GOARM64", "v8.0")}

# The Windows launcher (cmd/nanolathe-launcher) is a windowed program. It is
# not the game and carries no stamp of its own; online play never sees it.
LAUNCHER = "nanolathe-launcher.exe"
LAUNCHER_ARGS = ("-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -H windowsgui", "./cmd/nanolathe-launcher")
# The download a Windows player starts with: the x64 launcher, which also
# runs on ARM64 Windows and installs that machine's native build.
SETUP = "Nanolathe-Setup.exe"

# The release assets, in the order SHA256SUMS lists them.
ASSETS = ("nanolathe-windows-amd64.zip", "nanolathe-windows-arm64.zip", SETUP, "nanolathe-linux-amd64.tar.gz",
          "nanolathe-linux-arm64.tar.gz", "build.json", "SHA256SUMS")
# Files from the source tree that ship beside every executable.
NOTICES = ("LICENSE", "NOTICE.md")


class BuildError(Exception):
    pass


def executable(goos):
    return "nanolathe.exe" if goos == "windows" else "nanolathe"


def target_dir(goos, goarch):
    return f"{goos}-{goarch}"


def parse_manifest(text):
    """The installers' release manifest rules: key=value lines, no
    duplicates, and the Go version and six toolchain hashes present and
    well formed. Other keys stay inert strings."""
    values = {}
    for line in text.split("\n"):
        line = line.rstrip("\r")
        if not line or line.startswith("#"):
            continue
        match = re.fullmatch(r"([a-z][a-z0-9_]*)=([^\r\n]+)", line)
        if not match:
            raise BuildError("malformed release manifest line")
        key, value = match.groups()
        if key in values:
            raise BuildError(f"duplicate release manifest key: {key}")
        values[key] = value
    rules = {"go_version": r"[0-9]+\.[0-9]+\.[0-9]+"}
    for goos, goarch, _ in VARIANTS:
        rules[f"go_{goos}_{goarch}_sha256"] = r"[0-9a-fA-F]{64}"
    for key, rule in rules.items():
        if not re.fullmatch(rule, values.get(key, "")):
            raise BuildError(f"missing or invalid release manifest value: {key}")
    return values


def parse_version(version):
    """A Windows file version: four numbers, each 0-65535, without leading zeros."""
    parts = version.split(".")
    if len(parts) != 4 or not all(re.fullmatch(r"0|[1-9][0-9]*", p) and int(p) <= 65535 for p in parts):
        raise BuildError(f"version {version!r} is not four dot-separated numbers 0-65535")
    return version


def latest_release_version(tags):
    """The highest vX.Y.Z among tag names, as "X.Y.Z", or "0.0.0" without one."""
    best = (0, 0, 0)
    for tag in tags:
        match = re.fullmatch(r"v([0-9]+)\.([0-9]+)\.([0-9]+)", tag)
        if match:
            best = max(best, tuple(int(n) for n in match.groups()))
    return ".".join(str(n) for n in best)


def remote_tags(repository):
    output = subprocess.run(["git", "ls-remote", "--tags", "--refs", f"https://github.com/{repository}.git", "refs/tags/v*"],
                            check=True, capture_output=True, text=True).stdout
    return [line.split("refs/tags/", 1)[1] for line in output.splitlines() if "refs/tags/" in line]


def host_platform():
    goos = {"linux": "linux", "darwin": "darwin", "win32": "windows"}.get(sys.platform)
    goarch = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine().lower())
    if not goos or not goarch:
        raise BuildError(f"unsupported build host {sys.platform}/{platform.machine()}")
    return goos, goarch


def fetch(url, path):
    if urllib.parse.urlsplit(url).scheme != "https":
        raise BuildError(f"refusing a download without HTTPS: {url}")
    print(f"native-build: downloading {url}", flush=True)
    with urllib.request.urlopen(url, timeout=600) as response, open(path, "wb") as out:
        shutil.copyfileobj(response, out)


def sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def extract_tar(archive, destination):
    """Extract a tar.gz. Windows hosts skip symbolic links, which need
    privileges there and which the build does not use; the stamp reads the
    archive itself, links included."""
    def keep(member, path):
        if member.issym() and os.name == "nt":
            return None
        return tarfile.tar_filter(member, path)
    with tarfile.open(archive) as tar:
        tar.extractall(destination, filter=keep)


def read_manifest(source):
    if re.match(r"https?://", source):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "release.txt")
            fetch(source, path)
            return Path(path).read_text()
    return Path(source).read_text()


def go_environment(work, goroot):
    """The installers' cleared Go environment, with private caches."""
    env = dict(os.environ)
    for key in ("GOOS", "GOARCH", "GOAMD64", "GOARM64", "GOEXPERIMENT"):
        env.pop(key, None)
    env.update(CGO_ENABLED="0", GOTOOLCHAIN="local", GOENV="off", GOWORK="off", GOFLAGS="", GO111MODULE="on",
               GOROOT=str(goroot), GOPATH=str(work / "cache" / "gopath"),
               GOMODCACHE=str(work / "cache" / "gopath" / "pkg" / "mod"), GOCACHE=str(work / "cache" / "build"),
               GOPROXY="https://proxy.golang.org", GOSUMDB="sum.golang.org", GOPRIVATE="", GONOSUMDB="", GONOPROXY="")
    return env


def stamp_arguments(manifest, archive, prefix, source):
    args = [f"-arg={arg}" for arg in BUILD_ARGS]
    args += ["-source", str(archive), "-prefix", prefix, "-write", str(source), "-cgo", "0"]
    for goos, goarch, level in VARIANTS:
        args += ["-variant", f"{goos}/{goarch}/{level}={manifest[f'go_{goos}_{goarch}_sha256']}"]
    return args


def go(gobin, args, cwd, env, capture=False):
    result = subprocess.run([str(gobin), *args], cwd=cwd, env=env, text=True,
                            stdout=subprocess.PIPE if capture else None)
    if result.returncode != 0:
        raise BuildError(f"go {args[0]} failed with code {result.returncode}")
    return result.stdout


def reported_manifest(binary):
    """The build manifest digest a binary reports in its startup diagnostics.
    --check-install of an empty folder fails after writing them."""
    with tempfile.TemporaryDirectory() as empty:
        result = subprocess.run([str(binary), "--check-install", "--root", empty], capture_output=True, text=True)
    match = re.search(r"^nanolathe: build manifest: (\S+)", result.stderr, re.MULTILINE)
    return match.group(1) if match else None


def build(args):
    version = parse_version(args.version)
    manifest = parse_manifest(read_manifest(args.manifest))
    out = Path(args.out).resolve()
    if out.exists() and any(out.iterdir()):
        raise BuildError(f"{out} is not empty")
    out.mkdir(parents=True, exist_ok=True)
    work = Path(args.work).resolve() if args.work else Path(tempfile.mkdtemp(prefix="nanolathe-native-"))
    work.mkdir(parents=True, exist_ok=True)
    host_os, host_arch = host_platform()

    go_version = manifest["go_version"]
    ext = "zip" if host_os == "windows" else "tar.gz"
    toolchain = work / f"go{go_version}-{host_os}-{host_arch}"
    if not (toolchain / "go").is_dir():
        archive = work / f"go.{ext}"
        fetch(f"https://go.dev/dl/go{go_version}.{host_os}-{host_arch}.{ext}", archive)
        expected = manifest[f"go_{host_os}_{host_arch}_sha256"].lower()
        if sha256(archive) != expected:
            raise BuildError(f"SHA-256 verification failed: {archive}")
        unpack = work / "toolchain-unpack"
        if ext == "zip":
            with zipfile.ZipFile(archive) as z:
                z.extractall(unpack)
        else:
            extract_tar(archive, unpack)
        unpack.rename(toolchain)
    goroot = toolchain / "go"
    gobin = goroot / "bin" / ("go.exe" if host_os == "windows" else "go")

    repository_name = args.repository.split("/", 1)[1]
    archive = work / "source.tar.gz"
    if args.archive:
        shutil.copyfile(args.archive, archive)
    else:
        fetch(f"https://codeload.github.com/{args.repository}/tar.gz/{args.revision}", archive)
    prefix = f"{repository_name}-{args.revision}/"
    unpack = work / "source"
    if unpack.exists():
        shutil.rmtree(unpack)
    extract_tar(archive, unpack)
    source = unpack / prefix.rstrip("/")
    if not (source / "go.sum").is_file():
        raise BuildError("source archive is missing go.sum")
    sum_before = sha256(source / "go.sum")
    env = go_environment(work, goroot)

    print("native-build: stamping the build manifest", flush=True)
    stamp = go(gobin, ["run", "-mod=readonly", "-trimpath", "-buildvcs=false", "./internal/version/stampgen",
                       *stamp_arguments(manifest, archive, prefix, source)], source, env, capture=True).strip().splitlines()[-1]
    if not re.fullmatch(r"[0-9a-f]{64}", stamp):
        raise BuildError(f"stampgen printed {stamp!r}, not a manifest digest")
    print(f"native-build: build manifest {stamp}", flush=True)

    icon = source / "tools" / "installer" / "windows" / "Nanolathe.ico"
    for goos, goarch in TARGETS:
        if goos != "windows":
            continue
        for command, name, description in (("nanolathe", executable(goos), "Nanolathe"),
                                           ("nanolathe-launcher", LAUNCHER, "Nanolathe Launcher")):
            go(gobin, ["run", "-mod=readonly", "-trimpath", "-buildvcs=false", "./internal/version/winresgen",
                       "-arch", goarch, "-version", version, "-name", name, "-description", description,
                       "-icon", str(icon), "-out", str(source / "cmd" / command / f"rsrc_{goos}_{goarch}.syso")],
               source, env)
    for goos, goarch in TARGETS:
        print(f"native-build: building {goos}/{goarch}", flush=True)
        level, value = LEVELS[goarch]
        target_env = dict(env, GOOS=goos, GOARCH=goarch, **{level: value})
        destination = out / target_dir(goos, goarch)
        destination.mkdir()
        go(gobin, ["build", "-o", str(destination / executable(goos)), *BUILD_ARGS], source, target_env)
        if goos == "windows":
            go(gobin, ["build", "-o", str(destination / LAUNCHER), *LAUNCHER_ARGS], source, target_env)
    go(gobin, ["mod", "verify"], source, env)
    if sha256(source / "go.sum") != sum_before:
        raise BuildError("the build changed the pinned go.sum")

    if (host_os, host_arch) in TARGETS:
        binary = out / target_dir(host_os, host_arch) / executable(host_os)
        reported = reported_manifest(binary)
        if reported != stamp:
            raise BuildError(f"{binary} reports build manifest {reported!r}, stamped {stamp}")
        print(f"native-build: {host_os}/{host_arch} reports the stamped manifest", flush=True)
    else:
        print(f"native-build: no target runs on this {host_os}/{host_arch} host; its manifest is unchecked", flush=True)

    info = {"schema": 1, "repository": args.repository, "revision": args.revision, "version": version,
            "build_manifest": stamp, "go_version": go_version,
            "targets": [target_dir(goos, goarch) for goos, goarch in TARGETS]}
    if args.run_number is not None:
        info["run_number"] = args.run_number
    (out / "build.json").write_text(json.dumps(info, indent=2) + "\n")
    if not args.work:
        shutil.rmtree(work, ignore_errors=True)
    print(f"native-build: wrote {out}", flush=True)


# Fixed archive metadata, so the same inputs package to the same bytes.
ZIP_TIME = (1980, 1, 1, 0, 0, 0)


def package_zip(path, files):
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        for name, data, mode in files:
            info = zipfile.ZipInfo(name, ZIP_TIME)
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = (0o100000 | mode) << 16
            z.writestr(info, data)


def package_tar(path, files):
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w", format=tarfile.PAX_FORMAT) as tar:
        for name, data, mode in files:
            info = tarfile.TarInfo(name)
            info.size, info.mode, info.mtime = len(data), mode, 0
            tar.addfile(info, io.BytesIO(data))
    with open(path, "wb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", mtime=0, filename="") as gz:
        gz.write(buffer.getvalue())


def package(args):
    build_dir, out, root = Path(args.build), Path(args.out), Path(args.source_root)
    info = json.loads((build_dir / "build.json").read_text())
    if not re.fullmatch(r"[0-9a-f]{40}", info.get("revision", "")):
        raise BuildError("build.json has no source revision")
    out.mkdir(parents=True, exist_ok=True)
    # The game reads source-revision beside its executable, since a build
    # from an archive records no version-control revision (startup_report.go).
    common = [("source-revision", (info["revision"] + "\n").encode(), 0o644)]
    common += [(name, (root / name).read_bytes(), 0o644) for name in NOTICES]
    for goos, goarch in TARGETS:
        binary = build_dir / target_dir(goos, goarch) / executable(goos)
        files = [(executable(goos), binary.read_bytes(), 0o755), *common]
        if goos == "windows":
            # The launcher updates itself from the release it installs.
            files.insert(1, (LAUNCHER, (binary.parent / LAUNCHER).read_bytes(), 0o755))
            package_zip(out / f"nanolathe-{goos}-{goarch}.zip", files)
        else:
            package_tar(out / f"nanolathe-{goos}-{goarch}.tar.gz", files)
    shutil.copyfile(build_dir / target_dir("windows", "amd64") / LAUNCHER, out / SETUP)
    shutil.copyfile(build_dir / "build.json", out / "build.json")
    sums = "".join(f"{sha256(out / name)}  {name}\n" for name in ASSETS if name != "SHA256SUMS")
    (out / "SHA256SUMS").write_text(sums)
    print(sums, end="")


def build_main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--repository", default=os.environ.get("GITHUB_REPOSITORY", "nanolathe-gg/nanolathe"),
                        help="owner/name whose codeload archive is built, default $GITHUB_REPOSITORY")
    parser.add_argument("--revision", required=True, help="the full commit to build")
    parser.add_argument("--version", help="the Windows file version A.B.C.D; default the highest vX.Y.Z tag "
                                          "with the run number as its fourth field")
    parser.add_argument("--run-number", type=int, help="the workflow run number, recorded in build.json")
    parser.add_argument("--manifest", default=MANIFEST_URL, help="the installers' release manifest, a URL or file")
    parser.add_argument("--out", default="build/native", help="an empty output directory")
    parser.add_argument("--archive", help="build this local tar.gz instead of codeload's archive, to test unpushed "
                                          "changes; its one top directory must be <name>-<revision>/")
    parser.add_argument("--work", help="a directory to keep downloads, source and caches in (default: temporary)")
    args = parser.parse_args(argv)
    if not re.fullmatch(r"[0-9a-f]{40}", args.revision):
        parser.error("--revision must be a full 40-character commit")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repository):
        parser.error("--repository must be owner/name")
    if args.version is None:
        if args.run_number is None:
            parser.error("--version or --run-number is required")
        args.version = f"{latest_release_version(remote_tags(args.repository))}.{args.run_number}"
    try:
        build(args)
    except BuildError as error:
        print(f"native-build: {error}", file=sys.stderr)
        return 1
    return 0


def package_main(argv=None):
    parser = argparse.ArgumentParser(description="Package a native build directory into release assets and SHA256SUMS.")
    parser.add_argument("--build", default="build/native", help="the native-build output")
    parser.add_argument("--out", default="build/native-dist", help="where the release assets are written")
    parser.add_argument("--source-root", default=str(Path(__file__).resolve().parent.parent),
                        help="the source tree whose notices ship with the executables")
    args = parser.parse_args(argv)
    try:
        package(args)
    except BuildError as error:
        print(f"native-package: {error}", file=sys.stderr)
        return 1
    return 0
