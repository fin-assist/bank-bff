#!/usr/bin/env python3
"""Run FIN-437/Q90 against immutable neighboring services, with no runtime hooks."""

import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
from urllib.parse import parse_qsl, urlsplit

ROOT = Path(__file__).resolve().parents[1]
PINS = {
    "bsd": ("bank-statement-data", "f6d7c9a5e6ef62c6e97797f6899aec574b8f6ea1"),
    "analytics": ("analytics", "e00c6114d011f33576355dee19ecd77e1e7c4e1b"),
}


def git(source, *args):
    return subprocess.check_output(
        ["git", "-C", str(source), *args], text=True, timeout=15
    ).strip()


def verify(source, name):
    repo, sha = PINS[name]
    remote = git(source, "remote", "get-url", "origin")
    if remote not in (
        f"https://github.com/fin-assist/{repo}.git",
        f"https://github.com/fin-assist/{repo}",
        f"git@github.com:fin-assist/{repo}.git",
    ):
        raise ValueError(f"{name}: unexpected origin")
    if git(source, "rev-parse", "HEAD") != sha:
        raise ValueError(f"{name}: expected pinned full SHA {sha}")
    if git(source, "status", "--porcelain", "--untracked-files=all"):
        raise ValueError(f"{name}: fixture requires clean pinned source")


def run(args, cwd, env, timeout=240):
    # A process session owns go, test binaries and all fixture descendants. Tool
    # yield/poll intervals are not deadlines. No child detaches from this group.
    print(f"Q90 start {datetime.now(timezone.utc).isoformat()} {args[0:3]}", flush=True)
    process = subprocess.Popen(args, cwd=cwd, env=env, start_new_session=True)
    try:
        code = process.wait(timeout=timeout)
    finally:
        # Even a normal nonzero go exit (including -test.timeout) can leave a
        # fixture alive after its parent died before t.Cleanup. Reap every path.
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=10)
    print(f"Q90 end {datetime.now(timezone.utc).isoformat()} exit={code}", flush=True)
    if code:
        raise subprocess.CalledProcessError(code, args)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bsd", required=True, type=Path)
    parser.add_argument("--analytics", required=True, type=Path)
    args = parser.parse_args()
    if not os.environ.get("TEST_DATABASE_URL"):
        parser.error("TEST_DATABASE_URL is mandatory (isolated local PostgreSQL 18)")
    database = urlsplit(os.environ["TEST_DATABASE_URL"])
    overrides = {key.lower() for key, _ in parse_qsl(database.query)}
    if database.scheme not in ("postgres", "postgresql") or database.hostname not in (
        "localhost", "127.0.0.1", "::1"
    ) or overrides.intersection({"host", "hostaddr", "service", "servicefile"}):
        parser.error("Q90 requires a local test-owned PostgreSQL server")
    sources = {name: getattr(args, name).resolve() for name in PINS}
    for name, source in sources.items():
        verify(source, name)
    print(f"Q90 BFF: {git(ROOT, 'rev-parse', 'HEAD')} "
          f"dirty={bool(git(ROOT, 'status', '--porcelain', '--untracked-files=all'))}", flush=True)
    env = {**os.environ, "CI": "true", "GOPRIVATE": "github.com/fin-assist/*"}
    with tempfile.TemporaryDirectory(prefix="fin437-qase90-") as temp:
        directory = Path(temp)
        for name, source in sources.items():
            overlay = directory / f"{name}.json"
            overlay.write_text(json.dumps({"Replace": {
                str(source / "internal/app/qase90_fixture_test.go"):
                    str(ROOT / f"testdata/qase90/{name}_test.go.txt")
            }}))
            binary = directory / f"{name}.test"
            run(["go", "test", "-race", f"-overlay={overlay}", "-c", "-o", str(binary),
                 "./internal/app"], source, env)
            env[f"Q90_{name.upper()}_BINARY"] = str(binary)
            env[f"Q90_{name.upper()}_SOURCE"] = str(source)
            print(f"Q90 {name}: {PINS[name][1]}", flush=True)
        run(["go", "test", "-race", "-count=1", "-tags=qase90", "-timeout=3m", "-v",
             "-run=^TestAPIQase90CommittedDismissalSurvivesCompositionOutage$",
             "./internal/app"], ROOT, env)


if __name__ == "__main__":
    main()
