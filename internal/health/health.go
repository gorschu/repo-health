// Package health inspects local Git repositories and maintains an advisory cache.
package health

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
)

// Scan collects the Mani inventory into the cache without contacting remotes.
func Scan(ctx context.Context) error { return scan(ctx) }

// Inspect checks a repository and its linked worktrees. A nonempty recordDir
// writes the internal worker record instead of returning human-readable output.
func Inspect(ctx context.Context, path, recordDir string) (string, error) {
	repo := inspectRepo(ctx, path)
	if recordDir != "" {
		key := fmt.Sprintf("%x.json", sha256.Sum256([]byte(repo.ID)))
		return "", writeJSON(filepath.Join(recordDir, key), repo)
	}
	return describe(repo, nowSeconds()) + "\n", nil
}

// Display renders cached state; it never inspects repositories or uses the network.
func Display(mode, severity string) (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	return display(readReport(dir), mode, severity, nowSeconds()), nil
}
