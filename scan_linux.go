package main

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func scan(ctx context.Context) error {
	dir, err := cacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "scan.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil
		}
		return err
	}
	temporary, err := os.MkdirTemp(dir, "scan-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	self, err := os.Executable()
	if err != nil {
		return err
	}
	config := os.Getenv("MANI_CONFIG")
	if config == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		config = filepath.Join(home, ".config/mani/main.yaml")
	}
	var problems []string
	_, err = runCommand(ctx, 120*time.Second, []string{"REPO_HEALTH_RUN_DIR=" + temporary}, "mani",
		"--config", config, "exec", "--all", "--parallel", "--forks", "4", "--silent", "--color=false",
		shellQuote(self)+" inspect --record")
	if err != nil {
		problems = append(problems, "Mani scan incomplete: "+err.Error())
	}
	entries, err := os.ReadDir(temporary)
	if err != nil {
		return err
	}
	var repos []repository
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(temporary, entry.Name()))
		var repo repository
		if err != nil || json.Unmarshal(data, &repo) != nil || repo.ID == "" {
			problems = append(problems, "Mani scan incomplete: invalid worker report")
			continue
		}
		repos = append(repos, repo)
	}
	if len(repos) == 0 {
		problems = append(problems, "no repositories checked")
	}
	if len(problems) > 0 {
		// Preserve previous findings for unreachable inventory entries.
		ids := make(map[string]bool, len(repos))
		for _, repo := range repos {
			ids[repo.ID] = true
		}
		for _, repo := range readReport(dir).Repos {
			if !ids[repo.ID] {
				repos = append(repos, repo)
			}
		}
	}
	slices.SortFunc(repos, func(a, b repository) int { return cmp.Compare(a.Path, b.Path) })
	if err := writeJSON(filepath.Join(dir, "status.json"), report{Schema: schema, CheckedAt: nowSeconds(), Errors: problems, Repos: repos}); err != nil {
		return err
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
