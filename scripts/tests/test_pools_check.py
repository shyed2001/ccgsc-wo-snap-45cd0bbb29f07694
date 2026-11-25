#!/usr/bin/env python3
"""Unit tests of scripts/pools_check.py (no network, no real client run).

What/why: WO native hybrid apps (2026-11-21). The check runner is the
evidence producer of a lab machine, so its command line, its secret
handling, its summary line and its exit codes are pinned here. The
subprocess layer is mocked: no test starts a server and no test reaches the
network.
"""

from __future__ import annotations

import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

SCRIPTS_DIR = Path(__file__).resolve().parent.parent
if str(SCRIPTS_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPTS_DIR))

import pools_check  # noqa: E402  (path set up above)


def setUpModule():
    """Silence the progress output of the script under test."""
    sys.stdout = io.StringIO()


def tearDownModule():
    """Restore stdout."""
    sys.stdout = sys.__stdout__


class FakeCompleted:
    """Minimal stand-in for subprocess.CompletedProcess."""

    def __init__(self, returncode=0, stdout="", stderr=""):
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


PASS_REPORT = {
    "mode": "full",
    "base": "http://127.0.0.1:8091",
    "pools": ["/thread-pool/v1", "/process-pool/v1"],
    "checks": [
        {"id": "thread/artifact-upload", "check": "artifact-upload", "status": "PASS"},
        {"id": "thread/rest-submit", "check": "rest-submit", "status": "PASS"},
        {"id": "process/rest-submit", "check": "rest-submit", "status": "PASS"},
    ],
    "pass": True,
    "revokedKeys": 2,
    "startedAt": "2026-11-21T00:00:00Z",
    "durationMs": 1234,
}

FAIL_REPORT = {
    "mode": "full",
    "base": "http://127.0.0.1:8091",
    "pools": ["/thread-pool/v1"],
    "checks": [
        {"id": "thread/artifact-upload", "check": "artifact-upload", "status": "PASS"},
        {"id": "thread/rest-submit", "check": "rest-submit", "status": "FAIL"},
    ],
    "pass": False,
    "revokedKeys": 1,
    "startedAt": "2026-11-21T00:00:00Z",
    "durationMs": 900,
}


def fake_run(written_report=None, returncode=0, stdout="", stderr="", record=None):
    """A fake subprocess.run that writes the client report into the folder.

    The client is expected to write the report to the path given by -out, so
    the fake parses the command line to find it.
    """

    def _run(cmd, **kwargs):
        if record is not None:
            record.append({"cmd": cmd, "kwargs": kwargs})
        if written_report is not None and "-out" in cmd:
            out_path = Path(cmd[cmd.index("-out") + 1])
            out_path.parent.mkdir(parents=True, exist_ok=True)
            out_path.write_text(json.dumps(written_report), encoding="utf-8")
        return FakeCompleted(returncode=returncode, stdout=stdout, stderr=stderr)

    return _run


class SummaryTests(unittest.TestCase):
    """The one-line summary of the evidence folder."""

    def test_pass_report_gives_a_pass_summary(self):
        summary = pools_check.build_summary(
            PASS_REPORT, "http://127.0.0.1:8091", "/tmp/pools-client", "check-report.json"
        )
        self.assertTrue(summary.startswith("PASS "))
        self.assertIn("checks=3/3", summary)
        self.assertIn("revokedKeys=2", summary)
        self.assertIn("durationMs=1234", summary)
        self.assertEqual(len(summary.splitlines()), 1)

    def test_fail_report_gives_a_fail_summary(self):
        summary = pools_check.build_summary(
            FAIL_REPORT, "http://127.0.0.1:8091", "/tmp/pools-client", "check-report.json"
        )
        self.assertTrue(summary.startswith("FAIL "))
        self.assertIn("checks=1/2", summary)

    def test_count_checks_ignores_checks_without_a_status(self):
        passed, total = pools_check.count_checks(
            {"checks": [{"status": "PASS"}, {}, None]}
        )
        self.assertEqual(passed, 1)
        self.assertEqual(total, 3)


class ResolveClientTests(unittest.TestCase):
    """The client binary is found in the distribution folder."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.addCleanup(self.tmp.cleanup)

    def test_explicit_client_must_exist(self):
        with self.assertRaises(pools_check.UsageError):
            pools_check.resolve_client(str(self.root / "missing-client"))

    def test_no_client_anywhere_is_a_usage_error(self):
        with mock.patch.object(pools_check, "candidate_clients", lambda: []):
            with self.assertRaises(pools_check.UsageError):
                pools_check.resolve_client(None)

    def test_the_client_of_the_distribution_folder_is_used(self):
        binary = self.root / "dist" / "linux_amd64" / "pools-client"
        binary.parent.mkdir(parents=True, exist_ok=True)
        binary.write_bytes(b"elf")
        with mock.patch.object(pools_check, "module_dir", lambda: self.root / "pools-core"):
            with mock.patch.object(pools_check, "candidate_clients", pools_check.candidate_clients):
                found = pools_check.resolve_client(None)
        self.assertEqual(found, binary)


class RunTests(unittest.TestCase):
    """The end-to-end flow of the check runner."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.addCleanup(self.tmp.cleanup)
        self.client = self.root / "pools-client"
        self.client.write_bytes(b"elf")
        self.key_file = self.root / "admin.key"
        self.key_file.write_text("super-secret-key-value\n", encoding="utf-8")
        self.out = self.root / "evidence"
        self.record = []

    def _run(self, argv, report=PASS_REPORT, returncode=0, stdout="", stderr=""):
        with mock.patch.object(pools_check, "resolve_client", lambda explicit: self.client):
            with mock.patch.object(
                pools_check.subprocess,
                "run",
                fake_run(report, returncode, stdout, stderr, self.record),
            ):
                return pools_check.run(argv)

    def test_a_passing_suite_exits_with_zero_and_writes_the_evidence(self):
        code = self._run(
            ["--base", "http://127.0.0.1:8091", "--key-file", str(self.key_file), "--out", str(self.out)]
        )
        self.assertEqual(code, 0)
        report_file = self.out / "check-report.json"
        summary_file = self.out / "summary.txt"
        self.assertTrue(report_file.is_file())
        self.assertTrue(summary_file.is_file())
        summary = summary_file.read_text(encoding="utf-8").strip()
        self.assertTrue(summary.startswith("PASS "))
        self.assertIn("checks=3/3", summary)
        self.assertEqual(len(summary.splitlines()), 1)

    def test_a_failing_suite_exits_with_one_but_still_writes_the_evidence(self):
        code = self._run(
            ["--base", "http://127.0.0.1:8091", "--key-file", str(self.key_file), "--out", str(self.out)],
            report=FAIL_REPORT,
            returncode=1,
        )
        self.assertEqual(code, 1)
        self.assertTrue((self.out / "check-report.json").is_file())
        summary = (self.out / "summary.txt").read_text(encoding="utf-8").strip()
        self.assertTrue(summary.startswith("FAIL "))
        self.assertIn("checks=1/2", summary)

    def test_the_client_is_called_with_the_expected_arguments(self):
        self._run(
            ["--base", "http://x:1", "--key-file", str(self.key_file), "--out", str(self.out)]
        )
        cmd = self.record[0]["cmd"]
        self.assertEqual(cmd[0], str(self.client))
        self.assertIn("-base", cmd)
        self.assertIn("-key-file", cmd)
        self.assertIn("check", cmd)
        self.assertEqual(cmd[cmd.index("-base") + 1], "http://x:1")
        self.assertEqual(cmd[cmd.index("-key-file") + 1], str(self.key_file))
        self.assertEqual(cmd[cmd.index("-pool") + 1], "both")

    def test_the_key_value_is_never_printed_or_stored(self):
        secret = "super-secret-key-value"
        self._run(
            ["--base", "http://127.0.0.1:8091", "--key-file", str(self.key_file), "--out", str(self.out)]
        )
        for path in self.out.rglob("*"):
            if path.is_file():
                self.assertNotIn(secret, path.read_text(encoding="utf-8", errors="replace"))
        # The key file path is passed on, the key value never is.
        cmd = self.record[0]["cmd"]
        self.assertNotIn(secret, cmd)

    def test_a_missing_key_file_is_a_usage_error(self):
        code = self._run(
            ["--base", "http://127.0.0.1:8091", "--key-file", str(self.root / "nope.key"), "--out", str(self.out)]
        )
        self.assertEqual(code, 2)
        self.assertEqual(self.record, [])

    def test_a_missing_key_file_flag_is_a_usage_error(self):
        code = self._run(["--base", "http://127.0.0.1:8091", "--out", str(self.out)])
        self.assertEqual(code, 2)
        self.assertEqual(self.record, [])

    def test_a_missing_client_is_a_usage_error(self):
        with mock.patch.object(pools_check, "resolve_client", mock.Mock(
            side_effect=pools_check.UsageError("no pools-client binary found")
        )):
            code = pools_check.run(
                ["--base", "http://127.0.0.1:8091", "--key-file", str(self.key_file), "--out", str(self.out)]
            )
        self.assertEqual(code, 3)

    def test_a_client_without_a_report_exits_with_four(self):
        code = self._run(
            ["--base", "http://127.0.0.1:8091", "--key-file", str(self.key_file), "--out", str(self.out)],
            report=None,
            returncode=2,
            stderr="boom\n",
        )
        self.assertEqual(code, 4)
        self.assertTrue((self.out / "client-stderr.txt").is_file())

    def test_the_evidence_folder_is_created_when_missing(self):
        target = self.root / "deep" / "evidence"
        code = self._run(
            ["--base", "http://127.0.0.1:8091", "--key-file", str(self.key_file), "--out", str(target)]
        )
        self.assertEqual(code, 0)
        self.assertTrue(target.is_dir())

    def test_the_stdout_and_stderr_of_the_client_are_stored(self):
        self._run(
            ["--base", "http://127.0.0.1:8091", "--key-file", str(self.key_file), "--out", str(self.out)],
            stdout="PASS  3/3 checks passed\n",
            stderr="",
        )
        stdout_file = self.out / "client-stdout.txt"
        self.assertTrue(stdout_file.is_file())
        self.assertIn("3/3 checks passed", stdout_file.read_text(encoding="utf-8"))

    def test_a_stale_pass_report_is_not_reused_when_the_client_fails(self):
        """R10 fix lane: chatgpt MAJOR row (pools_check.py).

        The evidence folder may hold the PASS report of an earlier run. A client
        that then dies without writing a report must not hand that old PASS back
        as this run's verdict, which made the check exit 0.
        """
        self.out.mkdir(parents=True, exist_ok=True)
        (self.out / "check-report.json").write_text(json.dumps(PASS_REPORT), encoding="utf-8")
        code = self._run(
            ["--base", "http://127.0.0.1:8091", "--key-file", str(self.key_file), "--out", str(self.out)],
            report=None,
            returncode=1,
            stderr="client failed\n",
        )
        self.assertEqual(code, 4)
        self.assertFalse((self.out / "check-report.json").is_file())

    def test_a_client_that_died_after_a_pass_report_never_exits_zero(self):
        """R10 fix lane: chatgpt MAJOR row (pools_check.py).

        A client that dies abnormally is not a verdict, even when it managed to
        write a passing report before the crash. The runner must not exit 0.
        """
        def _die_after_report(cmd, **kwargs):
            if "-out" in cmd:
                out_path = Path(cmd[cmd.index("-out") + 1])
                out_path.parent.mkdir(parents=True, exist_ok=True)
                out_path.write_text(json.dumps(PASS_REPORT), encoding="utf-8")
            return FakeCompleted(returncode=124, stdout="", stderr="timed out\n")

        with mock.patch.object(pools_check, "resolve_client", lambda explicit: self.client):
            with mock.patch.object(pools_check.subprocess, "run", _die_after_report):
                code = pools_check.run(
                    ["--base", "http://127.0.0.1:8091", "--key-file", str(self.key_file), "--out", str(self.out)]
                )
        self.assertEqual(code, 4)


if __name__ == "__main__":
    unittest.main()
