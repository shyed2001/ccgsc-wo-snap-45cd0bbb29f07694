#!/usr/bin/env python3
"""Unit tests of scripts/build_native.py (no network, no real Go build).

What/why: WO native hybrid apps (2026-11-21). The build script is the tool
every lab machine uses, so its target selection, its Go flags, its failure
handling and the SHA256SUMS.txt manifest are pinned here. The subprocess
layer is mocked: no test builds a real binary and no test touches the
network.
"""

from __future__ import annotations

import contextlib
import functools
import hashlib
import io
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

SCRIPTS_DIR = Path(__file__).resolve().parent.parent
if str(SCRIPTS_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPTS_DIR))

import build_native  # noqa: E402  (path set up above)


class FakeCompleted:
    """Minimal stand-in for subprocess.CompletedProcess."""

    def __init__(self, returncode=0, stdout="", stderr=""):
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


def fake_go_run(calls, fail_for=None, produce=True):
    """A fake subprocess.run that records the calls and writes the output."""

    def _run(cmd, **kwargs):
        calls.append({"cmd": cmd, "kwargs": kwargs})
        if fail_for is not None and fail_for in " ".join(cmd):
            return FakeCompleted(returncode=1, stderr="compile error\n")
        if produce:
            out_index = cmd.index("-o")
            out_path = Path(cmd[out_index + 1])
            out_path.parent.mkdir(parents=True, exist_ok=True)
            out_path.write_bytes(b"MZ fake binary")
        return FakeCompleted(returncode=0)

    return _run


def quiet(func):
    """Run a function with stdout and stderr captured, so the test output stays readable."""

    @functools.wraps(func)
    def wrapper(*args, **kwargs):
        buffer = io.StringIO()
        with contextlib.redirect_stdout(buffer), contextlib.redirect_stderr(buffer):
            result = func(*args, **kwargs)
        return result

    return wrapper


def setUpModule():
    """Silence the progress output of the scripts under test.

    unittest reports the results on stderr, so the stdout of the scripts
    under test is only noise in the test report.
    """
    sys.stdout = io.StringIO()


def tearDownModule():
    """Restore stdout."""
    sys.stdout = sys.__stdout__


class TargetSelectionTests(unittest.TestCase):
    """The --os / --arch filters select the wanted targets."""

    def test_no_filter_builds_every_supported_target(self):
        targets = build_native.resolve_targets(None, None)
        self.assertEqual(
            targets,
            [("windows", "amd64"), ("linux", "amd64"), ("linux", "arm64")],
        )

    def test_os_filter_keeps_every_arch_of_that_os(self):
        targets = build_native.resolve_targets(["windows"], None)
        self.assertEqual(targets, [("windows", "amd64")])

    def test_arch_filter_keeps_every_os_of_that_arch(self):
        targets = build_native.resolve_targets(None, ["arm64"])
        self.assertEqual(targets, [("linux", "arm64")])

    def test_os_and_arch_filter_is_the_cross_product(self):
        targets = build_native.resolve_targets(["linux"], ["amd64", "arm64"])
        self.assertEqual(targets, [("linux", "amd64"), ("linux", "arm64")])

    def test_all_keyword_means_every_supported_value(self):
        targets = build_native.resolve_targets(["all"], ["all"])
        self.assertEqual(len(targets), len(build_native.SUPPORTED_TARGETS))

    def test_comma_separated_values_are_split(self):
        targets = build_native.resolve_targets(["windows,linux"], ["amd64"])
        self.assertEqual(targets, [("windows", "amd64"), ("linux", "amd64")])

    def test_unsupported_os_is_a_usage_error(self):
        with self.assertRaises(build_native.UsageError):
            build_native.resolve_targets(["plan9"], None)

    def test_unsupported_arch_is_a_usage_error(self):
        with self.assertRaises(build_native.UsageError):
            build_native.resolve_targets(None, ["riscv64"])

    def test_unsupported_pair_is_a_usage_error(self):
        # windows/arm64 is not a target of the lab fleet.
        with self.assertRaises(build_native.UsageError):
            build_native.resolve_targets(["windows"], ["arm64"])


class NamingTests(unittest.TestCase):
    """The binary and folder names follow the lab fleet convention."""

    def test_windows_binaries_get_the_exe_suffix(self):
        self.assertEqual(build_native.binary_name("pools-client", "windows"), "pools-client.exe")

    def test_linux_binaries_get_no_suffix(self):
        self.assertEqual(build_native.binary_name("pools-client", "linux"), "pools-client")

    def test_target_folder_is_os_underscore_arch(self):
        self.assertEqual(build_native.target_dir_name("linux", "arm64"), "linux_arm64")


class BuildCommandTests(unittest.TestCase):
    """The go build command carries the required flags."""

    def test_command_uses_trimpath_stripped_ldflags_and_the_app_package(self):
        out_path = Path("/tmp/out/pools-client")
        cmd = build_native.build_command("go", out_path, "pools-client")
        self.assertEqual(cmd[0], "go")
        self.assertEqual(cmd[1], "build")
        self.assertIn("-trimpath", cmd)
        self.assertIn("-ldflags", cmd)
        self.assertEqual(cmd[cmd.index("-ldflags") + 1], "-s -w")
        self.assertEqual(cmd[-1], "./cmd/pools-client")
        self.assertEqual(Path(cmd[cmd.index("-o") + 1]), out_path)

    def test_environment_disables_cgo_and_pins_the_toolchain(self):
        env = build_native.build_environment("linux", "arm64")
        self.assertEqual(env["GOOS"], "linux")
        self.assertEqual(env["GOARCH"], "arm64")
        self.assertEqual(env["CGO_ENABLED"], "0")
        self.assertEqual(env["GOTOOLCHAIN"], "local")

    def test_every_app_of_the_mvp_is_built(self):
        self.assertEqual(
            build_native.APPS,
            ("pools-client", "pools-e2e", "pools-core"),
        )


class BuildBinaryTests(unittest.TestCase):
    """The build step reports failures instead of writing a broken artifact."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.addCleanup(self.tmp.cleanup)

    def test_successful_build_returns_the_output_path(self):
        calls = []
        with mock.patch.object(build_native.subprocess, "run", fake_go_run(calls)):
            cmd, out_path = build_native.build_binary(
                ("linux", "amd64"), "pools-client", self.root, "go"
            )
        self.assertEqual(len(calls), 1)
        self.assertTrue(out_path.is_file())
        self.assertEqual(out_path.parent.name, "linux_amd64")
        self.assertEqual(out_path.name, "pools-client")
        self.assertEqual(calls[0]["kwargs"]["env"]["GOOS"], "linux")

    def test_failed_build_raises_build_error(self):
        with mock.patch.object(
            build_native.subprocess, "run", fake_go_run([], fail_for="pools-client")
        ):
            with self.assertRaises(build_native.BuildError):
                build_native.build_binary(("linux", "amd64"), "pools-client", self.root, "go")

    def test_dry_run_does_not_touch_the_file_system(self):
        calls = []
        with mock.patch.object(build_native.subprocess, "run", fake_go_run(calls)):
            _, out_path = build_native.build_binary(
                ("linux", "amd64"), "pools-client", self.root, "go", dry_run=True
            )
        self.assertEqual(calls, [])
        self.assertFalse(out_path.exists())


class ManifestTests(unittest.TestCase):
    """SHA256SUMS.txt lists every built binary with a relative name."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.addCleanup(self.tmp.cleanup)

    def _write_binary(self, folder, name, data):
        path = self.root / folder / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        return path

    def test_manifest_lists_the_binaries_with_relative_names(self):
        self._write_binary("windows_amd64", "pools-client.exe", b"win")
        self._write_binary("linux_arm64", "pools-core", b"linux")
        sums_path = build_native.write_sha256sums(self.root)
        lines = sums_path.read_text(encoding="utf-8").splitlines()
        self.assertEqual(len(lines), 2)
        names = [line.split("  ", 1)[1] for line in lines]
        self.assertEqual(names, ["linux_arm64/pools-core", "windows_amd64/pools-client.exe"])
        for line in lines:
            digest = line.split("  ", 1)[0]
            self.assertEqual(len(digest), 64)

    def test_manifest_hashes_match_the_file_content(self):
        path = self._write_binary("linux_amd64", "pools-e2e", b"payload")
        sums_path = build_native.write_sha256sums(self.root)
        content = sums_path.read_text(encoding="utf-8")
        expected = hashlib.sha256(b"payload").hexdigest()
        self.assertIn(expected, content)
        self.assertIn("linux_amd64/pools-e2e", content)

    def test_manifest_ignores_unrelated_files(self):
        self._write_binary("linux_amd64", "pools-client", b"x")
        self._write_binary("linux_amd64", "README.txt", b"not a binary")
        self._write_binary("linux_amd64", "pools-client.exe", b"y")
        entries = build_native.manifest_entries(self.root)
        names = sorted(name for name, _ in entries)
        self.assertEqual(names, ["linux_amd64/pools-client", "linux_amd64/pools-client.exe"])

    def test_manifest_uses_unix_separators(self):
        self._write_binary("linux_amd64", "pools-client", b"x")
        sums_path = build_native.write_sha256sums(self.root)
        self.assertNotIn("\\", sums_path.read_text(encoding="utf-8"))


class RunTests(unittest.TestCase):
    """The end-to-end flow of the script: build, manifest, exit codes."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.addCleanup(self.tmp.cleanup)
        self.out = self.root / "dist"

    def test_successful_run_writes_the_binaries_and_the_manifest(self):
        calls = []
        with mock.patch.object(build_native.subprocess, "run", fake_go_run(calls)):
            with mock.patch.object(build_native.shutil, "which", lambda name: "/usr/bin/go"):
                code = build_native.run(["--out", str(self.out)])
        self.assertEqual(code, 0)
        # 3 targets x 3 apps.
        self.assertEqual(len(calls), 9)
        for folder in ("windows_amd64", "linux_amd64", "linux_arm64"):
            self.assertTrue((self.out / folder).is_dir())
            self.assertTrue((self.out / folder / "pools-client").is_file() or
                            (self.out / folder / "pools-client.exe").is_file())
        sums = (self.out / "SHA256SUMS.txt").read_text(encoding="utf-8")
        self.assertEqual(len(sums.splitlines()), 9)
        for line in sums.splitlines():
            self.assertNotIn("\\", line)

    def test_os_and_arch_flags_limit_the_build(self):
        calls = []
        with mock.patch.object(build_native.subprocess, "run", fake_go_run(calls)):
            with mock.patch.object(build_native.shutil, "which", lambda name: "/usr/bin/go"):
                code = build_native.run(
                    ["--os", "linux", "--arch", "arm64", "--out", str(self.out)]
                )
        self.assertEqual(code, 0)
        self.assertEqual(len(calls), 3)
        self.assertTrue((self.out / "linux_arm64").is_dir())
        self.assertFalse((self.out / "windows_amd64").exists())

    def test_build_failure_exits_with_one(self):
        with mock.patch.object(
            build_native.subprocess, "run", fake_go_run([], fail_for="pools-e2e")
        ):
            with mock.patch.object(build_native.shutil, "which", lambda name: "/usr/bin/go"):
                code = build_native.run(["--out", str(self.out)])
        self.assertEqual(code, 1)

    def test_missing_go_toolchain_exits_with_three(self):
        with mock.patch.object(build_native.shutil, "which", lambda name: None):
            code = build_native.run(["--out", str(self.out)])
        self.assertEqual(code, 3)

    def test_unsupported_target_exits_with_two(self):
        code = build_native.run(["--os", "plan9", "--out", str(self.out)])
        self.assertEqual(code, 2)

    def test_dry_run_prints_the_commands_and_builds_nothing(self):
        calls = []
        with mock.patch.object(build_native.subprocess, "run", fake_go_run(calls)):
            code = build_native.run(["--dry-run", "--out", str(self.out)])
        self.assertEqual(code, 0)
        self.assertEqual(calls, [])
        self.assertFalse((self.out / "SHA256SUMS.txt").exists())

    def test_default_out_dir_is_the_dist_folder_next_to_the_module(self):
        self.assertEqual(
            build_native.default_out_dir(),
            build_native.module_dir().parent / "dist",
        )


class PathTests(unittest.TestCase):
    """The script finds the Go module from its own location."""

    def test_module_dir_is_the_parent_of_the_scripts_folder(self):
        self.assertEqual(build_native.module_dir(), SCRIPTS_DIR.parent)

    def test_module_dir_holds_the_go_mod_file(self):
        self.assertTrue((build_native.module_dir() / "go.mod").is_file())


if __name__ == "__main__":
    unittest.main()
