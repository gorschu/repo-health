# repo-health

[![CI](https://github.com/gorschu/repo-health/actions/workflows/ci.yml/badge.svg)](https://github.com/gorschu/repo-health/actions/workflows/ci.yml)

Local Git reminders for repositories managed by [Mani](https://manicli.com).
Catch work left on this machine before moving to another one.

```text
 Local work: 3 repos · 1 stash · 2 branches without push targets · checked 2m ago   mani run repo-check
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

## Install

Download the archive for your architecture and `checksums.txt` from
[GitHub Releases](https://github.com/gorschu/repo-health/releases).

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
| `repo-health summary` | Print cached counts and check age on one line |
| `repo-health show` | Read the detailed cached report |
| `repo-health version` | Print the version, commit and commit date |

`MANI_CONFIG` defaults to `~/.config/mani/main.yaml`. The cache is
`$XDG_CACHE_HOME/repo-health/status.json`, or `~/.cache/repo-health/status.json`.
Visible prompts and summaries include the check age. Missing, invalid or
more-than-ten-minute-old reports remain visible as warnings.
`prompt --severity error` and `prompt --severity warning` support separate
Starship modules if desired.

Use `repo-health --help` or `repo-health COMMAND --help` for command help.
`repo-health completion bash`, `zsh`, `fish` and `powershell` generate shell
completion scripts for installation using your shell's usual mechanism.

## Integrations

Examples are included under [`integrations/`](integrations/):

- A systemd user service and timer that scan about every three minutes.
- A `mani run repo-check` task for fresh, detailed local checks.
- A Starship module that only reads the cache.

Use `repo-health summary` in a Fastfetch command module. Prompt rendering does
not scan repositories. Personal paths, scheduling and styling belong in your
dotfiles; adjust the example service's `PATH` for your Git and Mani installation.

## Development

Install [mise](https://mise.jdx.dev), Git and a C compiler. The compiler is needed
for Go's race detector. The project pins Go, Task, GoReleaser, Mani and the
workflow linter in [`.mise.toml`](.mise.toml), which CI uses too.

```sh
mise trust
mise install
mise exec -- task build
mise exec -- task check
mise exec -- task --list
```

If mise is activated in your shell, run `task` directly. `task build` writes
`bin/repo-health`; `task check` checks formatting, runs Go vet and workflow lint,
then runs the native tests with the race detector.

The tests create isolated Git repositories and Mani inventories. They cover
staging, unusual filenames, stashes, unpublished branches, linked worktrees,
merge conflicts, partial clones, cancellation, cache warnings and scan recovery.
They also verify that inspection preserves Git refs, status and index contents.

The entry point lives in `cmd/repo-health`, the Cobra interface in `internal/cli`,
and repository inspection, scans and reports in `internal/health`. Tests live
beside the code they exercise.

## Releases

GoReleaser builds stripped, static Linux amd64 and arm64 executables,
with source paths removed and version metadata embedded. Timestamps derive from
the tagged commit, and SHA256 checksums accompany the archives. With a consistent
Go toolchain and source tree, this avoids build-time-dependent metadata.

```sh
# Validate and package locally without publishing:
mise exec -- task release:snapshot

# Publish a new version through GitHub Actions:
git tag -a v0.1.0 -m 'Release v0.1.0'
git push origin v0.1.0
```

CI runs formatting, vet, workflow lint and race tests, validates GoReleaser,
and builds snapshot archives. Pull requests that change only Markdown files skip
these steps, unless the files are under `cmd/` or `internal/`. Pushes to `main`,
version tags and manual runs always run all checks.

Version-tag pushes publish after validation succeeds. Pull requests and ordinary branch pushes do not publish.
Actions are pinned to full commit hashes, checkout credentials are not persisted,
and only the release job receives `contents: write`. The GoReleaser action uses
the version installed from `.mise.toml`.

[Renovate](https://docs.renovatebot.com/modules/manager/mise/) tracks mise tool
versions, Go module dependencies and pinned GitHub Actions. Minor and patch
updates are grouped by dependency type; major upgrades remain separate for
review. CI checks update pull requests; updates are not automatically merged.
