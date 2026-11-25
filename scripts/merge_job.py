"""3-way merge a lane job's work-folder copy of pools-core back into the repo (Tier 0, no AI).

File: CCGS_WASM/pools-core/scripts/merge_job.py. Created 2026-11-18 by Claude Opus 5.5 for the owner.
Why: parallel lane jobs (T42 F1..F10) each edit a copy taken at a base commit; merging by hand costs head-model
tokens. This finds every file the job changed against the base commit, normalizes CRLF to LF (Windows working
copies are CRLF and make whole-file false conflicts), and runs `git merge-file` (repo = ours, base, job = theirs).
New files are copied. Files listed in the job report are not needed; the base commit is the truth.

Usage: python merge_job.py <job pools-core dir> <base commit> <label>
Prints one line per file: new | merged | CONFLICT. Exit 1 if any conflict is left (markers stay in the file).
"""
import pathlib
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent  # pools-core
GITROOT = REPO.parent.parent  # Webassembly
SKIP = {"graphify-out", "node_modules", ".git", ".ai", "PROGRESS.log", "REPORT.md"}


def lf(b: bytes) -> bytes:
    return b.replace(b"\r\n", b"\n")


def base_blob(rel: str, base: str) -> bytes | None:
    r = subprocess.run(["git", "show", f"{base}:CCGS_WASM/pools-core/{rel}"], cwd=GITROOT, capture_output=True, check=False)
    return r.stdout if r.returncode == 0 else None


def main() -> int:
    job, base, label = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
    tmp = job.parent / "_merge_base"
    tmp.mkdir(exist_ok=True)
    conflicts = 0
    for f in sorted(job.rglob("*")):
        if not f.is_file() or SKIP & set(f.relative_to(job).parts):
            continue
        rel = f.relative_to(job).as_posix()
        theirs, b = lf(f.read_bytes()), base_blob(rel, base)
        mine = REPO / rel
        if b is not None and lf(b) == theirs:
            continue  # job did not change it
        if b is None:
            if not mine.exists():
                mine.parent.mkdir(parents=True, exist_ok=True)
                mine.write_bytes(theirs)
                print("new     ", rel)
            elif lf(mine.read_bytes()) != theirs:
                conflicts += 1
                print("CONFLICT", rel, "(new in job and in repo)")
            continue
        bp, tp = tmp / "base", tmp / "theirs"
        bp.write_bytes(lf(b))
        tp.write_bytes(theirs)
        mine.write_bytes(lf(mine.read_bytes()))
        rc = subprocess.run(["git", "merge-file", "-L", "repo", "-L", "base", "-L", label,
                             str(mine), str(bp), str(tp)], check=False).returncode
        conflicts += rc > 0
        print("CONFLICT" if rc else "merged  ", rel)
    return 1 if conflicts else 0


if __name__ == "__main__":
    sys.exit(main())
