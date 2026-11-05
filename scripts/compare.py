#!/usr/bin/env python3
"""Compare both implementations with isolated caches; never installs anything."""

import json
import os
from pathlib import Path
import statistics
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
COMMANDS = {
    "Python": [sys.executable, "-B", str(ROOT / "reference/repo-health.py")],
    "Go": [str(ROOT / "bin/repo-health")],
}


def normalize(value):
    if isinstance(value, dict):
        return {key: normalize(item) for key, item in value.items() if key != "checked_at"}
    if isinstance(value, list):
        return [normalize(item) for item in value]
    return value


def main():
    with tempfile.TemporaryDirectory(prefix="repo-health-comparison-") as temporary:
        env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
        reports = {}
        environments = {}
        for name, command in COMMANDS.items():
            environments[name] = dict(env, XDG_CACHE_HOME=str(Path(temporary) / name))
            subprocess.run(command + ["scan"], env=environments[name], check=True)
            path = Path(environments[name]["XDG_CACHE_HOME"]) / "repo-health/status.json"
            reports[name] = json.loads(path.read_text())
        if normalize(reports["Python"]) != normalize(reports["Go"]):
            raise SystemExit("Reports differ. Check for concurrent edits before investigating a port mismatch.")
        print(f"Identical findings across {len(reports['Go']['repos'])} repositories (timestamps excluded).")

        # Both implementations read the same cache for the prompt comparison.
        shared = environments["Python"]
        prompts = [subprocess.check_output(command + ["prompt"], env=shared) for command in COMMANDS.values()]
        if prompts[0] != prompts[1]:
            raise SystemExit("Prompt output differs.")
        print("Identical prompt output.")
        print("\nMedian wall time, milliseconds (warm runs; includes process startup):")
        print("Operation\tPython\tGo")
        for operation, count in (("prompt", 40), ("scan", 5)):
            samples = {name: [] for name in COMMANDS}
            # Interleave implementations to reduce bias from changing system load.
            for _ in range(count):
                for name, command in COMMANDS.items():
                    started = time.perf_counter()
                    subprocess.run(command + [operation], env=environments[name], check=True, stdout=subprocess.DEVNULL)
                    samples[name].append((time.perf_counter() - started) * 1000)
            print(operation + "\t" + "\t".join(f"{statistics.median(samples[name]):.2f}" for name in COMMANDS))
        print("\nLive cache, timer, shell configuration and installed Python checker were untouched.")


if __name__ == "__main__":
    main()
