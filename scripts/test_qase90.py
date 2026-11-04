"""An exited/timed-out go parent must not leave its fixture process running."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import qase90


class Qase90ProcessTests(unittest.TestCase):
    def test_descendant_releases_owned_lock_on_every_exit_path(self):
        child = (
            "import fcntl,sys,time; "
            "lock=open(sys.argv[1],'w'); fcntl.flock(lock,fcntl.LOCK_EX); "
            "print('locked',flush=True); time.sleep(60)"
        )
        for outcome in ("success", "failed", "timeout"):
            with self.subTest(outcome=outcome), tempfile.TemporaryDirectory() as temp:
                lock = Path(temp) / "owned.lock"
                parent = (
                    "import subprocess,sys,time; "
                    "child=subprocess.Popen([sys.executable,'-c',sys.argv[1],sys.argv[2]],stdout=subprocess.PIPE); "
                    "assert child.stdout.readline()==b'locked\\n'; "
                    + ("time.sleep(60)" if outcome == "timeout" else
                       f"sys.exit({7 if outcome == 'failed' else 0})")
                )
                command = [sys.executable, "-c", parent, child, str(lock)]
                if outcome == "success":
                    qase90.run(command, temp, os.environ, timeout=2)
                elif outcome == "failed":
                    with self.assertRaises(subprocess.CalledProcessError):
                        qase90.run(command, temp, os.environ, timeout=2)
                else:
                    with self.assertRaises(subprocess.TimeoutExpired):
                        qase90.run(command, temp, os.environ, timeout=2)
                # The actual kernel lock, not a mocked kill call, is the oracle.
                # SIGKILL delivery is asynchronous: block on the actual lock
                # with a process deadline, rather than assuming a schedule.
                subprocess.run([
                    sys.executable, "-c",
                    "import fcntl,sys; lock=open(sys.argv[1],'a'); "
                    "fcntl.flock(lock,fcntl.LOCK_EX)", str(lock)
                ], check=True, timeout=2)


if __name__ == "__main__":
    unittest.main()
