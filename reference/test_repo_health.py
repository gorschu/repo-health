"""Real Git/Mani integration tests; all repositories and configuration are temporary."""

import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name("repo-health.py")
loader = importlib.machinery.SourceFileLoader("repo_health", str(SCRIPT))
spec = importlib.util.spec_from_loader(loader.name, loader)
health = importlib.util.module_from_spec(spec)
loader.exec_module(health)


class RepoHealthTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="repo-health-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "home").mkdir()
        self.env = patch.dict(os.environ, {
            "HOME": str(self.root / "home"), "XDG_CONFIG_HOME": str(self.root / "config"),
            "XDG_CACHE_HOME": str(self.root / "cache"), "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_AUTHOR_NAME": "Test",
            "GIT_AUTHOR_EMAIL": "test@example.invalid", "GIT_COMMITTER_NAME": "Test",
            "GIT_COMMITTER_EMAIL": "test@example.invalid", "GIT_ALLOW_PROTOCOL": "file",
        })
        self.env.start()
        self.addCleanup(self.env.stop)
        self.remote = self.root / "remote.git"
        self.repo = self.root / "project"
        self.git(self.root, "init", "--bare", str(self.remote))
        self.git(self.root, "init", "-b", "main", str(self.repo))
        (self.repo / "tracked").write_text("initial\n")
        self.git(self.repo, "add", ".")
        self.git(self.repo, "commit", "-m", "initial")
        self.git(self.repo, "remote", "add", "origin", str(self.remote))
        self.git(self.repo, "push", "-u", "origin", "main")

    def git(self, path, *args):
        return subprocess.check_output(["git", "-C", str(path), *args], stderr=subprocess.DEVNULL)

    def save_report(self, repo, errors=None, age=0):
        health.write_json(health.cache_dir() / "status.json", {
            "schema": health.SCHEMA, "checked_at": time.time() - age,
            "errors": errors or [], "repos": [repo],
        })

    def display(self, mode="prompt", severity=None):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            health.display(mode, severity)
        return out.getvalue()

    def test_clean_local_repo_is_quiet_and_head_unchanged(self):
        before = self.git(self.repo, "rev-parse", "HEAD")
        report = health.inspect_repo(self.repo)
        self.assertEqual(report["errors"], [])
        self.assertEqual(health.issues(report, time.time()), [])
        self.assertEqual(self.git(self.repo, "rev-parse", "HEAD"), before)
        self.save_report(report)
        self.assertEqual(self.display(), "")

    def test_staging_untracked_rename_and_binary_files_are_read_only(self):
        (self.repo / "tracked").write_text("staged\n")
        self.git(self.repo, "add", "tracked")
        (self.repo / "tracked").write_text("unstaged\n")
        (self.repo / "odd\nname\t.bin").write_bytes(bytes(range(256)))
        before = self.git(self.repo, "status", "--porcelain=v2", "-z")
        index = (self.repo / ".git/index").read_bytes()
        report = health.inspect_repo(self.repo)
        tree = report["worktrees"][0]
        self.assertEqual((tree["staged"], tree["unstaged"], tree["untracked"], tree["files"]), (1, 1, 1, 2))
        self.assertEqual(self.git(self.repo, "status", "--porcelain=v2", "-z"), before)
        self.assertEqual((self.repo / ".git/index").read_bytes(), index)
        self.git(self.repo, "reset", "--hard", "HEAD")
        self.git(self.repo, "mv", "tracked", "renamed")
        self.assertEqual(health.inspect_repo(self.repo)["worktrees"][0]["files"], 2)

    def test_staging_only_change_is_detected(self):
        (self.repo / "tracked").write_text("edit\n")
        self.assertEqual(health.inspect_repo(self.repo)["worktrees"][0]["staged"], 0)
        self.git(self.repo, "add", "tracked")
        result = health.inspect_repo(self.repo)["worktrees"][0]
        self.assertEqual((result["staged"], result["unstaged"]), (1, 0))

    def test_unpublished_commit_on_non_current_branch(self):
        self.git(self.repo, "checkout", "-b", "feature")
        self.git(self.repo, "push", "-u", "origin", "feature")
        (self.repo / "tracked").write_text("feature\n")
        self.git(self.repo, "commit", "-am", "feature")
        self.git(self.repo, "checkout", "main")
        report = health.inspect_repo(self.repo)
        feature = next(b for b in report["branches"] if b["name"] == "feature")
        self.assertEqual(feature["ahead"], 1)
        self.save_report(report)
        self.assertIn("Local work: 1 repo", self.display())
        self.assertIn("1 branch ahead", self.display("summary"))

    def test_missing_destination_is_explicit(self):
        self.git(self.repo, "branch", "unpublished")
        report = health.inspect_repo(self.repo)
        self.assertTrue(any("unpublished" in e for e in health.issues(report, time.time())))

    def test_deleted_remote_branch_is_not_clean(self):
        self.git(self.repo, "branch", "feature")
        self.git(self.repo, "push", "-u", "origin", "feature")
        self.git(self.remote, "update-ref", "-d", "refs/heads/feature")
        self.git(self.repo, "fetch", "--prune", "origin")
        report = health.inspect_repo(self.repo)
        self.assertTrue(any("feature" in e for e in health.issues(report, time.time())))

    def test_linked_worktree_stashes_and_detached_head(self):
        linked = self.root / "linked"
        self.git(self.repo, "worktree", "add", "--detach", str(linked))
        (linked / "unsaved").write_text("work\n")
        (self.repo / "tracked").write_text("stash me\n")
        self.git(self.repo, "stash", "push")
        report = health.inspect_repo(self.repo)
        self.assertEqual(len(report["worktrees"]), 2)
        self.assertEqual(report["stashes"], 1)
        self.assertEqual(health.totals(report)["files"], 1)
        self.assertIn("detached HEAD", health.describe(report))

    def test_unavailable_remote_does_not_affect_local_checks(self):
        self.remote.rename(self.root / "offline.git")
        (self.repo / "tracked").write_text("unfinished work\n")
        self.git(self.repo, "stash", "push")
        (self.repo / "local").write_text("untracked\n")
        result = health.inspect_repo(self.repo)
        self.assertEqual(health.issues(result, time.time()), [])
        self.assertNotIn("remotes", result)
        self.save_report(result)
        self.assertIn("1 stash", self.display(severity="warning"))
        self.assertIn("Local work: 1 repo", self.display())
        self.assertNotIn("fetch", self.display("show"))

    def test_external_fetch_updates_next_local_comparison(self):
        (self.repo / "tracked").write_text("published elsewhere\n")
        self.git(self.repo, "commit", "-am", "work")
        other = self.root / "other"
        self.git(self.root, "clone", str(self.repo), str(other))
        self.git(other, "push", str(self.remote), "main")
        # Publishing through another checkout leaves this repo's tracking ref old.
        result = health.inspect_repo(self.repo)
        self.assertEqual(result["branches"][0]["ahead"], 1)
        self.git(self.repo, "fetch", "origin")
        result = health.inspect_repo(self.repo)
        self.assertEqual(result["branches"][0]["ahead"], 0)
        self.assertEqual(health.issues(result, time.time()), [])

    def test_incoming_commits_do_not_warn_about_remote_work(self):
        other = self.root / "other"
        self.git(self.root, "clone", "-b", "main", str(self.remote), str(other))
        (other / "tracked").write_text("incoming\n")
        self.git(other, "commit", "-am", "incoming")
        self.git(other, "push")
        self.git(self.repo, "fetch", "origin")
        self.save_report(health.inspect_repo(self.repo))
        self.assertEqual(self.display(), "")
        self.assertNotIn("updates", self.display("summary"))

    def test_stale_and_corrupt_cache_cannot_silence_warning(self):
        (self.repo / "local").write_text("work\n")
        report = health.inspect_repo(self.repo)
        self.save_report(report, age=601)
        self.assertIn("Local work", self.display())
        self.assertIn("stale", self.display())
        self.assertEqual(self.display(severity="warning"), "")
        (health.cache_dir() / "status.json").write_text("not json")
        self.assertIn("not checked yet", self.display())

    def test_triangular_push_target_is_checked(self):
        fork = self.root / "fork.git"
        self.git(self.root, "init", "--bare", str(fork))
        self.git(self.repo, "remote", "add", "fork", str(fork))
        self.git(self.repo, "push", "fork", "main")
        self.git(self.repo, "config", "remote.pushDefault", "fork")
        self.git(self.repo, "config", "push.default", "current")
        (self.repo / "tracked").write_text("only upstream\n")
        self.git(self.repo, "commit", "-am", "upstream only")
        self.git(self.repo, "push", "origin", "main")
        report = health.inspect_repo(self.repo)
        self.assertEqual(report["branches"][0]["remote"], "fork")
        self.assertEqual(report["branches"][0]["ahead"], 1)

    def test_partial_clone_cannot_lazily_fetch_missing_objects(self):
        self.git(self.remote, "config", "uploadpack.allowFilter", "true")
        partial = self.root / "partial"
        blob = self.git(self.repo, "rev-parse", "HEAD:tracked").decode().strip()
        self.git(self.root, "clone", "--no-checkout", "--filter=blob:none",
                 "-b", "main", self.remote.as_uri(), str(partial))
        with self.assertRaises(health.CheckError):
            health.git(partial, "cat-file", "-p", blob)
        # A normal Git command can still fetch the object; the checker opted out.
        self.assertEqual(self.git(partial, "cat-file", "-p", blob), b"initial\n")

    def test_unfinished_merge_is_visible(self):
        self.git(self.repo, "checkout", "-b", "feature")
        (self.repo / "tracked").write_text("feature\n")
        self.git(self.repo, "commit", "-am", "feature")
        self.git(self.repo, "checkout", "main")
        (self.repo / "tracked").write_text("main\n")
        self.git(self.repo, "commit", "-am", "main")
        with self.assertRaises(subprocess.CalledProcessError):
            self.git(self.repo, "merge", "feature")
        result = health.inspect_repo(self.repo)
        self.assertEqual(result["worktrees"][0]["conflicts"], 1)
        self.assertIn("operation in progress", health.describe(result))

    def test_timeout_is_bounded(self):
        started = time.monotonic()
        with self.assertRaisesRegex(health.CheckError, "timed out"):
            health.run(["/bin/sleep", "5"], timeout=0.05)
        self.assertLess(time.monotonic() - started, 2)

    def test_mani_collection_and_missing_project_do_not_hide_previous_work(self):
        if not shutil.which("mani"):
            self.skipTest("mani is not on PATH")
        config = self.root / "mani.yaml"
        config.write_text("projects:\n  test:\n    path: " + json.dumps(str(self.repo)) + "\n")
        os.environ["MANI_CONFIG"] = str(config)
        # A transport sentinel catches accidental fetching by any scan worker.
        marker = self.root / "ssh-called"
        ssh = self.root / "ssh"
        ssh.write_text('#!/bin/sh\ntouch "$REPO_HEALTH_TEST_MARKER"\nexit 1\n')
        ssh.chmod(0o700)
        os.environ.update(GIT_SSH_COMMAND=str(ssh), GIT_ALLOW_PROTOCOL="ssh",
                          REPO_HEALTH_TEST_MARKER=str(marker), REPO_HEALTH_FETCH="1")
        self.git(self.repo, "remote", "set-url", "origin", "ssh://git@example.invalid/project")
        (self.repo / "local").write_text("work\n")
        self.assertEqual(health.scan(), 0)
        self.assertEqual(len(health.report()["repos"]), 1)
        self.assertEqual(health.issues(health.report()["repos"][0], time.time()), [])
        self.assertFalse(marker.exists())
        self.assertIn("Local work", self.display())
        # Missing project must not be silently skipped or turn the old warning green.
        config.write_text("projects:\n  missing:\n    path: " + json.dumps(str(self.root / "absent")) + "\n")
        self.assertEqual(health.scan(), 1)
        self.assertIn("Local work", self.display())
        self.assertIn("incomplete", self.display())


if __name__ == "__main__":
    unittest.main()
