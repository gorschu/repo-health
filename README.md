# repo-health

[![CI](https://github.com/gorschu/repo-health/actions/workflows/ci.yml/badge.svg)](https://github.com/gorschu/repo-health/actions/workflows/ci.yml)

Local Git reminders for repositories managed by [Mani](https://manicli.com).
Catch work left on this machine before moving to another one.

```text
 Local work: 3 repos · 1 stash · 2 branches without push targets   mani run repo-check
```

The checker includes staged and unstaged changes, untracked files, stashes,
all local branches, linked worktrees, conflicts and unfinished Git operations.
It calls the installed Git executable and never fetches, pushes, commits,
changes branches or modifies staged files.

## Requirements

- Linux amd64 or arm64.
- Git with `GIT_NO_LAZY_FETCH` support, required to keep partial clones offline.
- [Mani](https://manicli.com) on `PATH` for inventory scans.
- A Nerd Font for the prompt icons.

The release binary has no Python, Go or shared-library runtime requirement.
The current development baseline is Git 2.56 and Mani 0.32.1.

## Install

Download the archive for your architecture and `checksums.txt` from
[GitHub Releases](https://github.com/gorschu/repo-health/releases).
While the repository is private, authenticate with `gh` to download assets:

```sh
gh release download --repo gorschu/repo-health \
  --pattern 'repo-health_*_linux_amd64.tar.gz' --pattern checksums.txt
sha256sum --check --ignore-missing checksums.txt
```

Extract the verified archive, then install its `repo-health` executable into
`~/.local/bin`. Use the `linux_arm64` archive on ARM machines.

For dotfile managers such as chezmoi, pin the release version and the corresponding
archive checksum. Release assets are named
`repo-health_VERSION_linux_ARCH.tar.gz` and contain the `repo-health` executable.

## Commands

| Command | Behavior |
| --- | --- |
| `repo-health scan` | Collect local state from every Mani repository into the cache |
| `repo-health inspect` | Check the current repository and its linked worktrees |
| `repo-health prompt` | Read the cache and print a compact reminder; silent when clean |
| `repo-health summary` | Read the cache and print counts and report age |
| `repo-health show` | Read the detailed cached report |
| `repo-health version` | Print the version, commit and commit date |

`MANI_CONFIG` defaults to `~/.config/mani/main.yaml`. The cache is
`$XDG_CACHE_HOME/repo-health/status.json`, or `~/.cache/repo-health/status.json`.
Missing, invalid or more-than-ten-minute-old reports remain visible.
`prompt --severity error` and `prompt --severity warning` support separate
Starship modules if desired.

## Integrations

Examples are included under [`integrations/`](integrations/):

- A systemd user service and timer that scan about every three minutes.
- A `mani run repo-check` task for fresh, detailed local checks.
- A muted Starship module that only reads the cache.

Use `repo-health summary` in a Fastfetch command module. Prompt rendering does
not scan repositories. Personal paths, scheduling and styling belong in your
dotfiles; adjust the example service's `PATH` for your Git and Mani installation.

## Scope and limits

Commit comparisons use the push destination's remote-tracking reference already
stored on disk. They cannot prove the server's current state. Normal fetches and
pushes update those references for the next scan. Branches without a resolvable
push target remain explicit warnings, including local backup branches.

Ignored files and repositories outside the Mani inventory are excluded. Counts
are advisory observations, not an atomic snapshot or a backup. An incomplete
inventory scan preserves previous findings for repositories it could not reach.
Git stderr is not persisted because it can contain sensitive remote URLs.

The implementation uses Linux process groups and file locking. Missing objects
in partial clones cause a check failure instead of an implicit fetch. The cache
requires UTF-8 repository paths and branch names; filenames inside the worktree
are counted using Git's NUL-delimited status output.

## Development

Go 1.27 or later is required. Tests use Git, Mani, Python 3 for reference
comparisons, and a C compiler for Go's race detector. There are no third-party
Go dependencies in the application.

```sh
make build
make check
make test
make compare
```

`make compare` runs Go and the frozen Python reference against your Mani
inventory using separate temporary caches. It compares findings and prompt
output and measures runtime, without changing the installed checker or live cache.
Concurrent repository edits can legitimately make the scans differ.

The native tests compare twelve Git scenarios against Python and cover cache
warnings, process cancellation, offline partial clones, scan locking and recovery.
`reference/` retains the original Python MVP and its sixteen tests for regression
checking; Python is not part of the distributed application.

## Releases

GoReleaser 2.20.0 builds stripped, static Linux amd64 and arm64 executables,
with source paths removed and version metadata embedded. Timestamps derive from
the tagged commit, and SHA256 checksums accompany the archives. With a consistent
Go toolchain and source tree, this avoids build-time-dependent metadata.

```sh
# Validate and package locally without publishing:
make dist

# Publish a new version through GitHub Actions:
git tag -a v0.1.0 -m 'Release v0.1.0'
git push origin v0.1.0
```

CI runs formatting, vet, race and Python reference tests, validates GoReleaser,
and builds snapshot archives. A version-tag push publishes only after that same
commit passes validation. Pull requests and ordinary branch pushes do not publish.
Actions are pinned to full commit hashes, checkout credentials are not persisted,
and only the release job receives `contents: write`. Dependabot proposes weekly
updates to the pinned actions. Update the GoReleaser version in the workflow
deliberately and run `make dist` when changing it.

The Go implementation follows the version-specific
[JetBrains Modern Go Guidelines](https://github.com/JetBrains/go-modern-guidelines).
