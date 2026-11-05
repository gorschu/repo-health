package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/gorschu/repo-health/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() { os.Exit(run()) }

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, cli.BuildInfo{Version: version, Commit: commit, Date: date})
}
