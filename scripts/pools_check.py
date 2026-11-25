#!/usr/bin/env python3
"""Run the native pools-client T108 check suite and store the evidence.

What/why: WO native hybrid apps (2026-11-21). One command runs the same
shared check suite that ``pools-client check`` runs (internal/checksuite:
2 pools x 3 APIs, REST + SSE + gRPC, anonymous contributor path and the T99
revocation of its own keys) against a running pool server and stores the
JSON report plus a one-line PASS/FAIL summary in an evidence folder, so a
lab machine can hand over a verifiable artifact.

Secret handling (T101): the API key is never read and never printed. The
script only passes the *path* of the key file to the client and lets the
client read it; the key value never enters this process. The captured
stdout/stderr are stored as evidence, never echoed to the console except in
the redacted failure path below.

Exit codes: 0 = suite PASS, 1 = suite FAIL, 2 = usage error, 3 = the
pools-client binary was not found, 4 = the client could not run.
"""

from __future__ import annotations

import argparse
import json
import os
import platform
import shutil
import subprocess
import sys
from pathlib import Path

# Name of the report and summary files inside the evidence folder.
REPORT_NAME = "check-report.json"
SUMMARY_NAME = "summary.txt"
STDOUT_NAME = "client-stdout.txt"
STDERR_NAME = "client-stderr.txt"

# Default timeout of the client run. The suite itself has its own timeout
# (-timeout); this one only guards against a client that never returns.
DEFAULT_TIMEOUT_SECONDS = 900


class UsageError(Exception):
    """A bad flag value or a missing required input."""


# --------------------------------------------------------------------------
# paths
# --------------------------------------------------------------------------


def script_dir() -> Path:
    """Folder of this script (pools-core/scripts)."""
    return Path(__file__).resolve().parent


def module_dir() -> Path:
    """Go module root (pools-core)."""
    return script_dir().parent


def host_target_dir():
    """Distribution folder name of the machine this script runs on."""
    machine = platform.machine().lower()
    arch = "amd64" if machine in ("amd64", "x86_64") else "arm64"
    os_name = "windows" if os.name == "nt" else "linux"
    return f"{os_name}_{arch}"


def candidate_clients():
    """Candidate pools-client binaries, most specific first.

    The binary of the machine this script runs on comes first: a Linux
    binary cannot be started on a Windows host, so a cross-built
    distribution folder must not be picked by accident.
    """
    names = ["pools-client.exe", "pools-client"] if os.name == "nt" else ["pools-client"]
    found = []

    dist_root = module_dir().parent / "dist"
    if dist_root.is_dir():
        subs = [sub for sub in dist_root.iterdir() if sub.is_dir()]
        host = host_target_dir()
        subs.sort(key=lambda sub: (sub.name != host, sub.name))
        for sub in subs:
            for name in names:
                path = sub / name
                if path.is_file():
                    found.append(path)

    bin_dir = module_dir() / "bin"
    for name in names:
        path = bin_dir / name
        if path.is_file():
            found.append(path)

    for name in names:
        path = Path(name)
        if path.is_file():
            found.append(path)

    on_path = shutil.which("pools-client")
    if on_path:
        found.append(Path(on_path))
    return found


def resolve_client(explicit):
    """Return the pools-client binary to run."""
    if explicit:
        path = Path(explicit).expanduser()
        if not path.is_file():
            raise UsageError(f"pools-client binary not found: {path}")
        return path
    env_bin = os.environ.get("POOLS_CLIENT_BIN")
    if env_bin:
        path = Path(env_bin).expanduser()
        if not path.is_file():
            raise UsageError(f"pools-client binary not found (POOLS_CLIENT_BIN): {path}")
        return path
    candidates = candidate_clients()
    if not candidates:
        raise UsageError(
            "no pools-client binary found; build one with build_native.py or pass --client"
        )
    return candidates[0]


# --------------------------------------------------------------------------
# argument handling
# --------------------------------------------------------------------------


def parse_args(argv=None):
    """Parse the command line of the check script."""
    parser = argparse.ArgumentParser(
        prog="pools_check.py",
        description="Run the pools-client T108 check suite and store the evidence.",
    )
    parser.add_argument(
        "--base",
        required=False,
        default=os.environ.get("POOLS_BASE_URL", "http://127.0.0.1:8091"),
        metavar="URL",
        help="pool server base URL (default: POOLS_BASE_URL or http://127.0.0.1:8091).",
    )
    parser.add_argument(
        "--key-file",
        required=False,
        default=os.environ.get("POOLS_KEY_FILE"),
        metavar="PATH",
        help="file that holds the admin API key; the key value is never read or printed here.",
    )
    parser.add_argument(
        "--out",
        required=True,
        metavar="DIR",
        help="evidence folder; it is created when missing.",
    )
    parser.add_argument(
        "--client",
        default=None,
        metavar="PATH",
        help="pools-client binary to run (default: the one in dist/ or bin/).",
    )
    parser.add_argument(
        "--pool",
        default="both",
        choices=("both", "thread", "process"),
        help="pools to check (default: both).",
    )
    parser.add_argument(
        "--size",
        type=int,
        default=160,
        metavar="N",
        help="matmul size of the submitted jobs (default: 160).",
    )
    parser.add_argument(
        "--timeout",
        default="3m",
        metavar="DURATION",
        help="timeout of the check suite itself, for example 3m (default: 3m).",
    )
    parser.add_argument(
        "--process-timeout",
        type=int,
        default=DEFAULT_TIMEOUT_SECONDS,
        metavar="SECONDS",
        help="hard guard of the client process (default: 900).",
    )
    return parser.parse_args(argv)


# --------------------------------------------------------------------------
# summary
# --------------------------------------------------------------------------


def count_checks(report):
    """(passed, total) of the checks in the report."""
    checks = report.get("checks") or []
    total = len(checks)
    passed = sum(1 for check in checks if (check or {}).get("status") == "PASS")
    return passed, total


def build_summary(report, base, client_path, report_name):
    """The one-line PASS/FAIL summary of the run."""
    passed, total = count_checks(report)
    verdict = "PASS" if report.get("pass") else "FAIL"
    duration = report.get("durationMs", 0)
    revoked = report.get("revokedKeys", 0)
    return (
        f"{verdict} pools-client check (T108) base={base} "
        f"checks={passed}/{total} durationMs={duration} revokedKeys={revoked} "
        f"client={client_path} report={report_name}"
    )


def write_summary(out_dir, text):
    """Write the one-line summary and return its path."""
    path = out_dir / SUMMARY_NAME
    with open(str(path), "w", encoding="utf-8", newline="\n") as handle:
        handle.write(text + "\n")
    return path


# --------------------------------------------------------------------------
# run
# --------------------------------------------------------------------------


def run_client(client_path, args, out_dir):
    """Run the client check suite.

    Returns (returncode, stdout, stderr, report_path). The report path is
    None when the client produced no JSON report. The stdout and stderr of
    the client are always stored in the evidence folder, also when the run
    timed out.

    R10 fix (chatgpt MAJOR row): the report of an earlier run in the same
    evidence folder is removed before the client starts, and an abnormal exit
    status is a failed run even when a report exists. Reusing the old PASS
    report made a failed run exit 0.
    """
    report_path = out_dir / REPORT_NAME
    try:
        report_path.unlink()
    except FileNotFoundError:
        pass
    except OSError as exc:
        raise UsageError(f"cannot clear the stale report {report_path}: {exc}")
    cmd = [
        str(client_path),
        "-base",
        args.base,
        "-key-file",
        args.key_file,
        "check",
        "-pool",
        args.pool,
        "-size",
        str(args.size),
        "-timeout",
        args.timeout,
        "-out",
        str(report_path),
    ]
    stdout = ""
    stderr = ""
    try:
        proc = subprocess.run(
            cmd,
            cwd=str(module_dir()),
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            universal_newlines=True,
            timeout=args.process_timeout,
        )
        returncode = proc.returncode
        stdout = proc.stdout
        stderr = proc.stderr
    except subprocess.TimeoutExpired as exc:
        returncode = 124
        stdout = exc.stdout or ""
        stderr = f"pools-client check timed out after {args.process_timeout}s"
        if isinstance(stdout, bytes):
            stdout = stdout.decode("utf-8", "replace")
    except OSError as exc:
        returncode = 125
        stderr = f"pools-client could not be started: {exc}"

    with open(str(out_dir / STDOUT_NAME), "w", encoding="utf-8", newline="\n") as handle:
        handle.write(stdout)
    with open(str(out_dir / STDERR_NAME), "w", encoding="utf-8", newline="\n") as handle:
        handle.write(stderr)

    if not report_path.is_file():
        return returncode, stdout, stderr, None
    return returncode, stdout, stderr, report_path


def load_report(report_path):
    """Read the JSON report of the client."""
    with open(str(report_path), "r", encoding="utf-8") as handle:
        return json.load(handle)


def run(argv=None):
    """Run the check. Returns the process exit code."""
    args = parse_args(argv)

    if not args.key_file:
        print(
            "pools_check: --key-file is required (the admin API key file); "
            "the key value is never read or printed by this script",
            file=sys.stderr,
        )
        return 2

    key_file = Path(args.key_file).expanduser()
    if not key_file.is_file():
        print(f"pools_check: key file not found: {key_file}", file=sys.stderr)
        return 2

    try:
        client_path = resolve_client(args.client)
    except UsageError as exc:
        print(f"pools_check: {exc}", file=sys.stderr)
        return 3

    out_dir = Path(args.out).expanduser()
    try:
        out_dir.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        print(f"pools_check: cannot create the evidence folder {out_dir}: {exc}", file=sys.stderr)
        return 2

    try:
        returncode, stdout, stderr, report_path = run_client(client_path, args, out_dir)
    except UsageError as exc:
        print(f"pools_check: {exc}", file=sys.stderr)
        return 2

    # R10 fix (chatgpt MAJOR row): the old report is already gone (run_client
    # removes it), so a client that wrote nothing cannot be handed a stale PASS.
    # The client's own verdict codes are 0 (PASS) and 1 (FAIL); any other status
    # is the runner dying (timeout 124, spawn failure 125, usage 2), and then even
    # a report it managed to write is not a verdict.
    if returncode not in (0, 1):
        print(
            "pools_check: the client exited with status "
            f"{returncode}; see {out_dir / STDERR_NAME}",
            file=sys.stderr,
        )
        return 4

    if report_path is None:
        print(
            "pools_check: the client produced no JSON report "
            f"(exit {returncode}); see {out_dir / STDERR_NAME}",
            file=sys.stderr,
        )
        if stderr.strip():
            print(stderr.strip(), file=sys.stderr)
        return 4

    try:
        report = load_report(report_path)
    except (OSError, ValueError) as exc:
        print(f"pools_check: the client report is not valid JSON: {exc}", file=sys.stderr)
        return 4

    summary = build_summary(report, args.base, client_path, REPORT_NAME)
    summary_path = write_summary(out_dir, summary)

    print(summary)
    print(f"evidence: {out_dir}")
    print(f"summary : {summary_path}")
    print(f"report  : {report_path}")

    if report.get("pass"):
        return 0
    return 1


def main(argv=None):
    """Console entry point."""
    return run(argv)


if __name__ == "__main__":
    sys.exit(main())
