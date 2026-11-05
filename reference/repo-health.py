#!/usr/bin/python3
"""Local-only Git reminders for Mani-managed repositories. Never contacts remotes."""

import argparse
from collections import Counter
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import sys
import tempfile
import time

SCHEMA = 2
LOCAL_TTL = 600


class CheckError(Exception):
    pass


def run(args, cwd=None, env=None, timeout=10):
    # Kill the process group on timeout, including any Mani workers.
    with subprocess.Popen(args, cwd=cwd, env=env, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, start_new_session=True) as child:
        try:
            out, _ = child.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            os.killpg(child.pid, signal.SIGKILL)
            child.communicate()
            raise CheckError("command timed out") from None
        if child.returncode:
            # Do not persist stderr, which can contain remote URLs/credentials.
            raise CheckError(f"command failed (exit {child.returncode})")
        return out


def git(path, *args, env=None, timeout=10):
    git_env = dict(os.environ, GIT_OPTIONAL_LOCKS="0", GIT_TERMINAL_PROMPT="0",
                   GCM_INTERACTIVE="never", GIT_NO_LAZY_FETCH="1")
    # Missing objects in partial clones must not trigger implicit network fetches.
    git_env.update(env or {})
    return run(["git", "--no-optional-locks", "-c", "core.fsmonitor=false",
                "-C", str(path), *args], env=git_env, timeout=timeout)


def text(data):
    return os.fsdecode(data).strip()


def safe(value):
    # Repo/branch names are data, not terminal control sequences.
    return "".join(c if c.isprintable() else "?" for c in str(value))


def cache_dir():
    return Path(os.environ.get("XDG_CACHE_HOME", str(Path.home() / ".cache"))) / "repo-health"


def read_json(path):
    try:
        value = json.loads(Path(path).read_text())
        return value if isinstance(value, dict) else {}
    except (OSError, ValueError):
        return {}


def write_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".report-")
    try:
        with os.fdopen(fd, "w") as stream:
            json.dump(value, stream, ensure_ascii=True)
            stream.write("\n")
        os.replace(tmp, path)
    finally:
        Path(tmp).unlink(missing_ok=True)


def report():
    value = read_json(cache_dir() / "status.json")
    return value if value.get("schema") == SCHEMA else {}


def branches(path):
    fmt = "%(refname)%00%(objectname)"
    rows = []
    for line in git(path, "for-each-ref", f"--format={fmt}", "refs/heads").splitlines():
        ref, oid = map(os.fsdecode, line.split(b"\0"))
        name = ref.removeprefix("refs/heads/")
        # @{push} handles triangular workflows and push.default. Resolve it per
        # branch so one branch without a destination does not hide all the rest.
        try:
            destination = text(git(path, "rev-parse", "--symbolic-full-name", f"{name}@{{push}}"))
            remote = text(git(path, "for-each-ref", "--format=%(push:remotename)", ref))
        except CheckError:
            destination, remote = "", ""
        rows.append({"name": name, "oid": oid, "destination": destination,
                     "remote": remote})
    return rows


def worktree_status(path):
    counts = {"staged": 0, "unstaged": 0, "untracked": 0, "conflicts": 0, "files": 0}
    entries = iter(git(path, "status", "--porcelain=v2", "-z", "--untracked-files=all",
                       "--ignore-submodules=none").split(b"\0"))
    for entry in entries:
        if not entry:
            continue
        kind = entry[:1]
        if kind in (b"1", b"2"):
            xy = entry.split(b" ", 2)[1]
            counts["staged"] += xy[:1] != b"."
            counts["unstaged"] += xy[1:] != b"."
            counts["files"] += 1
            if kind == b"2":
                next(entries)  # Original rename path is a separate NUL record.
        elif kind == b"u":
            counts["conflicts"] += 1
            counts["files"] += 1
        elif kind == b"?":
            counts["untracked"] += 1
            counts["files"] += 1
    operations = []
    for marker in ("MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "BISECT_LOG"):
        location = text(git(path, "rev-parse", "--path-format=absolute", "--git-path", marker))
        if Path(location).exists():
            operations.append(marker)
    try:
        branch = text(git(path, "symbolic-ref", "--short", "HEAD"))
    except CheckError:
        branch = "DETACHED HEAD"
    return {"path": str(path), **counts, "branch": branch, "operations": operations,
            "checked_at": time.time()}


def inspect_repo(path):
    path = Path(path).resolve()
    result = {"id": str(path), "path": str(path), "name": path.name, "errors": [],
              "worktrees": [], "branches": [], "stashes": 0}
    try:
        common = text(git(path, "rev-parse", "--path-format=absolute", "--git-common-dir"))
        result["id"] = common
        if text(git(path, "rev-parse", "--is-bare-repository")) == "true":
            raise CheckError("bare repository has no working tree")
        # Include linked worktrees even when they are not individually in Mani.
        for record in git(path, "worktree", "list", "--porcelain", "-z").split(b"\0\0"):
            fields = record.split(b"\0")
            if not fields[0].startswith(b"worktree "):
                continue
            worktree = Path(os.fsdecode(fields[0][9:]))
            try:
                result["worktrees"].append(worktree_status(worktree))
            except (CheckError, OSError) as error:
                result["errors"].append(f"worktree {safe(worktree)}: {error}")
        result["stashes"] = len(git(path, "stash", "list", "--format=%H").splitlines())
        # Compare only against tracking refs already stored on this machine.
        for branch in branches(path):
            branch.update(ahead=0, error="")
            if not branch["destination"].startswith("refs/remotes/") or not branch["remote"]:
                branch["error"] = "no configured push destination; publication cannot be verified"
            else:
                try:
                    branch["ahead"] = int(git(path, "rev-list", "--count",
                                              f'{branch["destination"]}..{branch["oid"]}'))
                except CheckError:
                    branch["error"] = "publication destination missing or unreadable"
            result["branches"].append(branch)
        if not result["branches"]:
            result["errors"].append("no local branch with a publication destination")
    except (CheckError, OSError, ValueError, StopIteration) as error:
        result["errors"].append(str(error) or "invalid Git output")
    result["checked_at"] = time.time()
    return result


def issues(repo, now):
    problems = list(repo.get("errors", []))
    if now - repo.get("checked_at", 0) > LOCAL_TTL or now < repo.get("checked_at", 0):
        problems.append("local check stale")
    for branch in repo.get("branches", []):
        if branch["error"]:
            problems.append(f'{branch["name"]}: {branch["error"]}')
    return problems


def totals(repo):
    trees = repo.get("worktrees", [])
    branches_ = repo.get("branches", [])
    return {"files": sum(w["files"] for w in trees),
            "ahead": sum(b["ahead"] > 0 for b in branches_),
            "stashes": repo.get("stashes", 0),
            "operations": sum(bool(w["operations"]) or w["branch"] == "DETACHED HEAD" for w in trees)}


def number(count, singular, plural=None):
    return f'{count} {singular if count == 1 else plural or singular + "s"}'


def check_messages(repos, now):
    """Name the reason a check needs attention instead of saying 'unknown'."""
    counts = Counter()
    for repo in repos:
        if repo.get("errors"):
            counts[("repo check failed", "repo checks failed")] += 1
        checked = repo.get("checked_at", 0)
        if now - checked > LOCAL_TTL or now < checked:
            counts[("repo check stale", "repo checks stale")] += 1
        for branch in repo.get("branches", []):
            if branch["error"]:
                if not branch["destination"].startswith("refs/remotes/") or not branch["remote"]:
                    counts[("branch without a push target", "branches without push targets")] += 1
                else:
                    counts[("branch comparison failed", "branch comparisons failed")] += 1
    return [number(value, *labels) for labels, value in counts.items()]


def describe(repo):
    lines = []
    for tree in repo.get("worktrees", []):
        parts = [f'{tree[k]} {k}' for k in ("staged", "unstaged", "untracked", "conflicts") if tree[k]]
        if tree["operations"]:
            parts.append("operation in progress")
        if tree["branch"] == "DETACHED HEAD":
            parts.append("detached HEAD")
        if parts:
            lines.append(f'{safe(tree["path"])}: ' + ", ".join(parts))
    for branch in repo.get("branches", []):
        if branch["ahead"]:
            lines.append(f'{safe(branch["name"])}: {branch["ahead"]} ahead of stored push target')
    if repo.get("stashes"):
        lines.append(number(repo["stashes"], "stash", "stashes"))
    lines.extend("Check needed: " + safe(p) for p in issues(repo, time.time()))
    return "\n".join(lines) or "clean; no commits ahead of stored push targets"


def scan():
    directory = cache_dir()
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (directory / "scan.lock").open("w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return 0
        with tempfile.TemporaryDirectory(prefix="scan-", dir=directory) as temporary:
            env = dict(os.environ, REPO_HEALTH_RUN_DIR=temporary)
            config_path = os.environ.get("MANI_CONFIG") or str(Path.home() / ".config/mani/main.yaml")
            command = shlex.join([sys.executable, str(Path(__file__).resolve()), "inspect", "--record"])
            errors = []
            try:
                # Each worker writes JSON directly; Mani's human output is not
                # an API. Missing projects/dispatch failures make the run incomplete.
                run(["mani", "--config", config_path, "exec", "--all", "--parallel", "--forks", "4",
                     "--silent", "--color=false", command], env=env, timeout=120)
            except (CheckError, OSError) as error:
                errors.append(f"Mani scan incomplete: {error}")
            repos = [read_json(p) for p in Path(temporary).glob("*.json")]
            repos = [r for r in repos if r.get("id")]
            if not repos:
                errors.append("no repositories checked")
            # Preserve warnings for repos that were not reached during a failed
            # run. A complete run naturally drops projects removed from Mani.
            if errors:
                ids = {r["id"] for r in repos}
                repos.extend(r for r in report().get("repos", []) if r["id"] not in ids)
            write_json(directory / "status.json", {"schema": SCHEMA, "checked_at": time.time(),
                       "errors": errors, "repos": sorted(repos, key=lambda r: r["path"])})
            if errors:
                print("; ".join(errors), file=sys.stderr)
            return int(bool(errors))


def display(mode, severity=None):
    data = report()
    now = time.time()
    problems = list(data.get("errors", []))
    if not data:
        problems.append("not checked yet")
    elif now - data.get("checked_at", 0) > LOCAL_TTL or now < data.get("checked_at", 0):
        problems.append("checker stale")
    repos = data.get("repos", [])
    checks = check_messages(repos, now)
    values = [totals(r) for r in repos]
    dirty = sum(bool(v["files"] or v["ahead"] or v["stashes"] or v["operations"]) for v in values)
    level = "error" if problems or checks else "warning" if dirty else "ok"
    if severity and level != severity:
        return
    pieces = []
    if dirty:
        pieces.append("Local work: " + number(dirty, "repo"))
    stashes = sum(v["stashes"] for v in values)
    if stashes:
        pieces.append(number(stashes, "stash", "stashes"))
    if mode != "prompt":
        files = sum(v["files"] for v in values)
        ahead = sum(v["ahead"] for v in values)
        if files:
            pieces.append(number(files, "file"))
        if ahead:
            pieces.append(number(ahead, "branch ahead", "branches ahead"))
    pieces.extend(checks)
    if problems:
        pieces.append("Check needed: " + ("scan incomplete" if data.get("errors") else "; ".join(map(safe, problems))))
    if mode == "prompt":
        if level != "ok":
            print(" " + " · ".join(pieces) + "   mani run repo-check")
        return
    print(" · ".join(pieces) or number(len(repos), "repo checked", "repos checked") + "; no outstanding work")
    if data.get("checked_at"):
        print(f'Local report: {max(0, int(now - data["checked_at"]))}s ago')
    if mode == "show":
        for problem in data.get("errors", []):
            print("Check needed: " + safe(problem))
        for repo in repos:
            print(f'\n{safe(repo["name"])} ({safe(repo["path"])}):\n{describe(repo)}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("scan", help="collect local state from all Mani repos into the cache")
    inspect_parser = sub.add_parser("inspect", help="check this repo without network access")
    inspect_parser.add_argument("--record", action="store_true", help=argparse.SUPPRESS)
    prompt_parser = sub.add_parser("prompt", help="cached global prompt warning")
    prompt_parser.add_argument("--severity", choices=["error", "warning"])
    sub.add_parser("show", help="cached detailed report")
    sub.add_parser("summary", help="cached Fastfetch summary")
    args = parser.parse_args()
    if args.command == "scan":
        return scan()
    if args.command == "inspect":
        repo = inspect_repo(Path.cwd())
        if args.record:
            directory = os.environ.get("REPO_HEALTH_RUN_DIR")
            if not directory:
                parser.error("--record requires a scan directory")
            key = hashlib.sha256(os.fsencode(repo["id"])).hexdigest()
            write_json(Path(directory) / f"{key}.json", repo)
        else:
            print(describe(repo))
        return 0  # Findings are data; collection errors are included in the report.
    display(args.command, getattr(args, "severity", None))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, KeyError, TypeError, AttributeError, IndexError) as error:
        # Fail visibly even if the cached report is corrupt or incompatible.
        print("Repo check failed: " + safe(error))
        sys.exit(1)
