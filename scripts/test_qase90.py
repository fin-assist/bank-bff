"""An exited, cancelled or timed-out go parent must not leave its fixture process running."""
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import unittest

import qase90


class Qase90ProcessTests(unittest.TestCase):
    def test_sigterm_of_runner_releases_descendant_lock(self):
        with tempfile.TemporaryDirectory() as temp:
            lock = Path(temp) / "cancelled.lock"
            child = (
                "import fcntl,sys,time; lock=open(sys.argv[1],'w'); "
                "fcntl.flock(lock,fcntl.LOCK_EX); print('locked',flush=True); time.sleep(60)"
            )
            parent = (
                "import os,subprocess,sys,time; "
                "child=subprocess.Popen([sys.executable,'-c',sys.argv[1],sys.argv[2]],stdout=subprocess.PIPE); "
                "assert child.stdout.readline()==b'locked\\n'; "
                "print('Q90_READY '+str(os.getpid()),flush=True); time.sleep(60)"
            )
            program = (
                "import os,qase90,sys; "
                "qase90.run([sys.executable,'-c',sys.argv[1],sys.argv[2],sys.argv[3]],"
                "os.getcwd(),os.environ,timeout=60)"
            )
            runner = subprocess.Popen(
                [sys.executable, "-u", "-c", program, parent, child, str(lock)],
                cwd=Path(__file__).resolve().parent,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True
            )
            ready = threading.Event()
            owned = []

            def observe():
                for line in runner.stdout:
                    if line.startswith(b"Q90_READY "):
                        owned.append(int(line.split()[1]))
                        ready.set()

            observer = threading.Thread(target=observe, daemon=True)
            observer.start()
            try:
                self.assertTrue(ready.wait(timeout=5), "runner's descendant did not acquire lock")
                runner.send_signal(signal.SIGTERM)
                self.assertEqual(runner.wait(timeout=5), 128 + signal.SIGTERM)
                # A different process blocks on the real kernel lock. It can
                # finish only after the cancelled runner cleaned its grandchild.
                subprocess.run([
                    sys.executable, "-c",
                    "import fcntl,sys; lock=open(sys.argv[1],'a'); "
                    "fcntl.flock(lock,fcntl.LOCK_EX)", str(lock)
                ], check=True, timeout=2)
            finally:
                # Test-failure cleanup also handles an unfixed runner: its inner
                # process session is deliberately independent from the runner.
                for group in owned + [runner.pid]:
                    try:
                        os.killpg(group, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                runner.wait(timeout=5)
                observer.join(timeout=5)
                runner.stdout.close()
                runner.stderr.close()

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
                previous = signal.getsignal(signal.SIGTERM)
                if outcome == "success":
                    qase90.run(command, temp, os.environ, timeout=2)
                elif outcome == "failed":
                    with self.assertRaises(subprocess.CalledProcessError):
                        qase90.run(command, temp, os.environ, timeout=2)
                else:
                    with self.assertRaises(subprocess.TimeoutExpired):
                        qase90.run(command, temp, os.environ, timeout=2)
                self.assertIs(signal.getsignal(signal.SIGTERM), previous)
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
