"""Public script checks; no real stores, credentials or network access."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("sync-mapped.sh").resolve()
MOCK = r'''#!/usr/bin/env python3
import json
import os
import sys
with open(os.environ["CALLS"], "a") as f:
    f.write(json.dumps({"args": sys.argv[1:], "cwd": os.getcwd(),
                        "db": os.environ["WIP_DB_PATH"],
                        "config": os.environ["XDG_CONFIG_HOME"]}) + "\n")
if sys.argv[1:] == ["plumbing", "sync", "bindings", "--json"]:
    print(os.environ["INVENTORY"])
    sys.exit(int(os.environ.get("INVENTORY_EXIT", "0")))
if sys.argv[1:4] != ["plumbing", "sync", "run"]:
    sys.exit(99)
print("private work payload")
if sys.argv[5] == os.environ.get("FAIL_REPO"):
    print("fixture pass failed", file=sys.stderr)
    sys.exit(1)
'''


class SyncMappedTest(unittest.TestCase):
    def invoke(self, mode="--list", inventory=None, **overrides):
        with tempfile.TemporaryDirectory(prefix="sync-mapped-") as scratch:
            root = Path(scratch)
            binary = root / "mock wip"
            binary.write_text(MOCK)
            binary.chmod(0o700)
            calls_path = root / "calls"
            env = dict(os.environ, WIP_BIN="./mock wip", CALLS=str(calls_path),
                       WIP_DB_PATH="relative selected.db", XDG_CONFIG_HOME="relative config",
                       INVENTORY=json.dumps(inventory if inventory is not None else {
                           "bindings": [self.binding("active one"),
                                        self.binding("paused", False), self.binding("active-two")]}))
            env.update(overrides)
            result = subprocess.run(["bash", str(SCRIPT), mode], cwd=root, env=env,
                                    text=True, capture_output=True)
            calls = [json.loads(line) for line in calls_path.read_text().splitlines()]
            for call in calls:
                self.assertEqual(call["cwd"], str(root))
                self.assertEqual(call["db"], "relative selected.db")
                self.assertEqual(call["config"], "relative config")
            self.assertNotIn("private work payload", result.stdout + result.stderr)
            return result, [call["args"] for call in calls]

    @staticmethod
    def binding(repo, active=True):
        return {"local_repo": repo, "active": active, "connection": "work",
                "account": "42", "endpoint": "https://work.invalid/api/organizations/1/repos/2"}

    def test_modes(self):
        for mode in ["--list", "--dry-run", "--run"]:
            with self.subTest(mode=mode):
                result, calls = self.invoke(mode)
                self.assertEqual(result.returncode, 0, result.stderr)
                expected = [["plumbing", "sync", "bindings", "--json"]]
                if mode != "--list":
                    expected += [["plumbing", "sync", "run", "--repo", repo, "--json"] +
                                 (["--dry-run"] if mode == "--dry-run" else [])
                                 for repo in ["active one", "active-two"]]
                self.assertEqual(calls, expected)
                self.assertIn("paused\tpaused\twork\t42\t", result.stdout)
                completed = 0 if mode == "--list" else 2
                self.assertIn(f"active=2 skipped=1 completed={completed} failed=0", result.stderr)

    def test_continue_after_failure(self):
        result, calls = self.invoke("--run", FAIL_REPO="active one")
        self.assertEqual(result.returncode, 1)
        self.assertEqual(len(calls), 3)
        self.assertIn("FAILED --run: active one (not retried)", result.stderr)
        self.assertIn("OK --run: active-two", result.stderr)
        self.assertIn("fixture pass failed", result.stderr)
        self.assertIn("completed=1 failed=1", result.stderr)

    def test_empty_and_paused_only(self):
        for bindings in [[], [self.binding("paused", False)]]:
            result, calls = self.invoke("--run", {"bindings": bindings})
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(calls, [["plumbing", "sync", "bindings", "--json"]])

    def test_bad_inventory_never_runs_passes(self):
        for inventory in [{}, {"bindings": None}, {"bindings": [{"local_repo": "partial"}]},
                          {"bindings": [self.binding("duplicate"), self.binding("duplicate")]},
                          {"bindings": [self.binding("bad\nrepo")]}]:
            result, calls = self.invoke("--run", inventory)
            self.assertEqual(result.returncode, 2)
            self.assertEqual(calls, [["plumbing", "sync", "bindings", "--json"]])
        for extra in [{"INVENTORY_EXIT": "1"}, {"INVENTORY": "invalid json"}]:
            result, calls = self.invoke("--run", **extra)
            self.assertEqual(result.returncode, 2)
            self.assertEqual(len(calls), 1)


if __name__ == "__main__":
    unittest.main()
