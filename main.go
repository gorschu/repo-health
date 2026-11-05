// repo-health reports local Git work without contacting remotes.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(entrypoint())
}

func entrypoint() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCLI(ctx, os.Args[1:], os.Stdout, os.Stderr)
}

func runCLI(ctx context.Context, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "Usage: repo-health {scan|inspect|prompt|summary|show|version}")
		return 2
	}
	command := args[0]
	if command == "--help" || command == "-h" {
		fmt.Fprintln(out, "Local-only Git reminders. Never contacts remotes.\n\nCommands:\n  scan      Collect all Mani repositories into the cache\n  inspect   Check this repository and its linked worktrees\n  prompt    Show the cached prompt reminder\n  summary   Show the cached Fastfetch summary\n  show      Show the detailed cached report\n  version   Print the build version\n\nEnvironment: MANI_CONFIG, XDG_CACHE_HOME")
		return 0
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	var record bool
	var severity string
	switch command {
	case "inspect":
		flags.BoolVar(&record, "record", false, "internal scan worker")
	case "prompt":
		flags.StringVar(&severity, "severity", "", "error or warning")
	case "scan", "show", "summary", "version":
	default:
		fmt.Fprintf(errOut, "Unknown command: %s\n", safe(command))
		return 2
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || (severity != "" && severity != "error" && severity != "warning") {
		fmt.Fprintln(errOut, "Unexpected argument or severity; use --help")
		return 2
	}
	var err error
	switch command {
	case "version":
		fmt.Fprintf(out, "repo-health %s\ncommit: %s\ncommit date: %s\n", version, commit, date)
	case "scan":
		err = scan(ctx)
	case "inspect":
		var path string
		path, err = os.Getwd()
		if err == nil {
			repo := inspectRepo(ctx, path)
			if record {
				dir := os.Getenv("REPO_HEALTH_RUN_DIR")
				if dir == "" {
					fmt.Fprintln(errOut, "--record requires a scan directory")
					return 2
				}
				key := fmt.Sprintf("%x.json", sha256.Sum256([]byte(repo.ID)))
				err = writeJSON(filepath.Join(dir, key), repo)
			} else {
				fmt.Fprintln(out, describe(repo, nowSeconds()))
			}
		}
	default:
		var dir string
		dir, err = cacheDir()
		if err == nil {
			fmt.Fprint(out, display(readReport(dir), command, severity, nowSeconds()))
		}
	}
	if err != nil {
		fmt.Fprintln(errOut, "Repo check failed: "+safe(err.Error()))
		return 1
	}
	return 0
}
