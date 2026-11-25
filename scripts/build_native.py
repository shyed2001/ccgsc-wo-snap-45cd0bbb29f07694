#!/usr/bin/env python3
"""Cross-build the native pools-core hybrid apps for the lab machines.

What/why: WO native hybrid apps (2026-11-21). One command produces the
windows/amd64, linux/amd64 and linux/arm64 binaries of the three native apps
(pools-client, pools-e2e, pools-core) into ``<out>/<os>_<arch>/`` and writes
``<out>/SHA256SUMS.txt`` with the relative names of every built binary.
CGO is off, the paths are trimmed and the symbol table is stripped, so the
same source tree yields the same artifact bytes on every lab machine.

Standard library only: the script runs on a bare lab machine with a Go
toolchain and a Python 3 interpreter, without an install step.

Exit codes: 0 = all binaries built, 1 = a build failed, 2 = usage error,
3 = the Go toolchain was not found.
"""

from __future__ import annotations

import argparse
import hashlib
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

# The three native hybrid apps of the MVP. pools-migrate is a maintenance
# tool of the main tree and is not part of the lab machine set.
APPS = ("pools-client", "pools-e2e", "pools-core")

# Supported target matrix of the lab fleet. The order is the build order and
# the order of the SHA256SUMS.txt lines.
SUPPORTED_TARGETS = (
    ("windows", "amd64"),
    ("linux", "amd64"),
    ("linux", "arm64"),
)

SUPPORTED_OS = ("windows", "linux")
SUPPORTED_ARCH = ("amd64", "arm64")

# A distribution folder is <os>_<arch>.
TARGET_DIR_RE = re.compile(r"^[a-z]+_[a-z0-9]+$")

# The Go flags of every build: reproducible paths, no symbol table, no
# debug info and no VCS stamping (the lab copy carries no repository).
GO_BUILD_FLAGS = ("-trimpath", "-ldflags", "-s -w", "-buildvcs=false")


class BuildError(Exception):
    """A build failure that is not a usage error."""


class UsageError(Exception):
    """A bad flag value or an unsupported target."""


# --------------------------------------------------------------------------
# paths
# --------------------------------------------------------------------------


def script_dir() -> Path:
    """Folder of this script (pools-core/scripts)."""
    return Path(__file__).resolve().parent


def module_dir() -> Path:
    """Go module root (pools-core)."""
    return script_dir().parent


def default_out_dir() -> Path:
    """Default distribution root: <work folder>/dist, next to pools-core."""
    return module_dir().parent / "dist"


def binary_name(app: str, os_name: str) -> str:
    """File name of one binary: .exe on Windows, no suffix elsewhere."""
    return app + ".exe" if os_name == "windows" else app


def target_dir_name(os_name: str, arch: str) -> str:
    """Distribution folder of one target: <os>_<arch>."""
    return f"{os_name}_{arch}"


# --------------------------------------------------------------------------
# argument handling
# --------------------------------------------------------------------------


def _split_values(values):
    """Flatten repeated and comma separated flag values."""
    out = []
    for value in values or ():
        for part in str(value).split(","):
            part = part.strip()
            if part:
                out.append(part)
    return out


def parse_args(argv=None):
    """Parse the command line of the build script."""
    parser = argparse.ArgumentParser(
        prog="build_native.py",
        description="Cross-build the native pools apps into <out>/<os>_<arch>/.",
    )
    parser.add_argument(
        "--os",
        action="append",
        dest="os_values",
        metavar="OS",
        help="target OS: windows, linux or all (repeatable, comma separated). Default: all supported targets.",
    )
    parser.add_argument(
        "--arch",
        action="append",
        dest="arch_values",
        metavar="ARCH",
        help="target architecture: amd64, arm64 or all (repeatable, comma separated). Default: all supported targets.",
    )
    parser.add_argument(
        "--out",
        default=None,
        metavar="DIR",
        help="distribution root; the binaries land in <out>/<os>_<arch>/ (default: <work folder>/dist).",
    )
    parser.add_argument(
        "--go",
        dest="go_bin",
        default=os.environ.get("POOLS_GO_BIN", "go"),
        metavar="PATH",
        help="go binary to use (default: go on PATH).",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="print the build commands without running them.",
    )
    return parser.parse_args(argv)


def resolve_targets(os_values, arch_values):
    """Return the ordered list of (os, arch) pairs to build.

    No filter means every supported target. Only --os means every supported
    target of that OS. Only --arch means every supported target of that
    architecture. Both filters mean their cross product, and an explicit
    pair that is not a supported target is a usage error instead of a silent
    skip. The "all" keyword means every supported value of that dimension;
    it never invents a pair outside the supported matrix.
    """
    oses = _split_values(os_values)
    arches = _split_values(arch_values)

    if not oses and not arches:
        return list(SUPPORTED_TARGETS)

    all_os = "all" in oses
    all_arch = "all" in arches
    wanted_os = list(SUPPORTED_OS) if all_os else oses
    wanted_arch = list(SUPPORTED_ARCH) if all_arch else arches

    for value in wanted_os:
        if value not in SUPPORTED_OS:
            raise UsageError(f"unsupported OS {value!r}; supported: {', '.join(SUPPORTED_OS)}")
    for value in wanted_arch:
        if value not in SUPPORTED_ARCH:
            raise UsageError(f"unsupported architecture {value!r}; supported: {', '.join(SUPPORTED_ARCH)}")

    if oses and arches:
        if all_os or all_arch:
            # A wildcard dimension can only narrow the supported matrix.
            return [t for t in SUPPORTED_TARGETS if t[0] in wanted_os and t[1] in wanted_arch]
        targets = []
        for os_name in wanted_os:
            for arch in wanted_arch:
                pair = (os_name, arch)
                if pair not in SUPPORTED_TARGETS:
                    supported = ", ".join(f"{o}/{a}" for o, a in SUPPORTED_TARGETS)
                    raise UsageError(f"unsupported target {os_name}/{arch}; supported: {supported}")
                targets.append(pair)
        return targets

    if oses:
        return [t for t in SUPPORTED_TARGETS if t[0] in wanted_os]

    return [t for t in SUPPORTED_TARGETS if t[1] in wanted_arch]


# --------------------------------------------------------------------------
# build
# --------------------------------------------------------------------------


def build_environment(os_name, arch, toolchain="local"):
    """Child environment of one go build."""
    env = os.environ.copy()
    env["GOOS"] = os_name
    env["GOARCH"] = arch
    env["CGO_ENABLED"] = "0"
    env["GOTOOLCHAIN"] = toolchain
    return env


def build_command(go_bin, out_path, app):
    """The go build command line of one binary."""
    cmd = [go_bin, "build"]
    cmd.extend(GO_BUILD_FLAGS)
    cmd.extend(["-o", str(out_path), f"./cmd/{app}"])
    return cmd


def build_binary(target, app, out_root, go_bin, dry_run=False, module=None, toolchain="local"):
    """Build one app for one target and return (command, output path)."""
    os_name, arch = target
    module = module or module_dir()
    out_dir = out_root / target_dir_name(os_name, arch)
    out_path = out_dir / binary_name(app, os_name)
    cmd = build_command(go_bin, out_path, app)

    if dry_run:
        return cmd, out_path

    out_dir.mkdir(parents=True, exist_ok=True)
    env = build_environment(os_name, arch, toolchain)
    proc = subprocess.run(
        cmd,
        cwd=str(module),
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        universal_newlines=True,
    )
    if proc.returncode != 0:
        raise BuildError(
            f"build {os_name}/{arch} {app} failed (exit {proc.returncode}):\n"
            f"{proc.stdout}{proc.stderr}"
        )
    if not out_path.is_file() or out_path.stat().st_size == 0:
        raise BuildError(f"build {os_name}/{arch} {app} produced no binary at {out_path}")
    return cmd, out_path


# --------------------------------------------------------------------------
# manifest
# --------------------------------------------------------------------------


def sha256_file(path):
    """SHA256 of one file as lowercase hex."""
    digest = hashlib.sha256()
    with open(str(path), "rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def manifest_entries(out_root):
    """(relative name, absolute path) of every built binary under out_root.

    Only the files of a <os>_<arch> folder whose stem is one of the native
    apps count, so an unrelated file in the distribution root is ignored.
    """
    entries = []
    if not out_root.is_dir():
        return entries
    for sub in sorted(out_root.iterdir()):
        if not sub.is_dir() or not TARGET_DIR_RE.match(sub.name):
            continue
        for path in sorted(sub.iterdir()):
            if path.is_file() and path.stem in APPS:
                entries.append((path.relative_to(out_root).as_posix(), path))
    return entries


def write_sha256sums(out_root):
    """Write <out>/SHA256SUMS.txt with relative names and return its path."""
    out_root.mkdir(parents=True, exist_ok=True)
    sums_path = out_root / "SHA256SUMS.txt"
    lines = []
    for rel, path in manifest_entries(out_root):
        lines.append(f"{sha256_file(path)}  {rel}\n")
    with open(str(sums_path), "w", encoding="utf-8", newline="\n") as handle:
        handle.writelines(lines)
    return sums_path


# --------------------------------------------------------------------------
# entry point
# --------------------------------------------------------------------------


def run(argv=None):
    """Run the build. Returns the process exit code."""
    args = parse_args(argv)
    try:
        targets = resolve_targets(args.os_values, args.arch_values)
    except UsageError as exc:
        print(f"build_native: {exc}", file=sys.stderr)
        return 2

    out_root = Path(args.out).resolve() if args.out else default_out_dir()
    module = module_dir()
    toolchain = os.environ.get("POOLS_GO_TOOLCHAIN", "local")

    if not args.dry_run:
        if shutil.which(args.go_bin) is None and not Path(args.go_bin).is_file():
            print(f"build_native: go toolchain not found: {args.go_bin}", file=sys.stderr)
            return 3
        if not (module / "go.mod").is_file():
            print(f"build_native: no go.mod in {module}", file=sys.stderr)
            return 2

    out_root.mkdir(parents=True, exist_ok=True)
    built = 0
    try:
        for target in targets:
            for app in APPS:
                cmd, out_path = build_binary(
                    target,
                    app,
                    out_root,
                    args.go_bin,
                    dry_run=args.dry_run,
                    module=module,
                    toolchain=toolchain,
                )
                if args.dry_run:
                    print(" ".join(cmd))
                else:
                    print(f"built {target[0]}/{target[1]} {app} -> {out_path}")
                built += 1
    except BuildError as exc:
        print(f"build_native: {exc}", file=sys.stderr)
        return 1

    if args.dry_run:
        return 0

    sums_path = write_sha256sums(out_root)
    print(f"built {built} binaries into {out_root}")
    print(f"sha256 sums: {sums_path}")
    return 0


def main(argv=None):
    """Console entry point."""
    return run(argv)


if __name__ == "__main__":
    sys.exit(main())
