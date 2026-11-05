package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type worktree struct {
	Path       string   `json:"path"`
	Staged     int      `json:"staged"`
	Unstaged   int      `json:"unstaged"`
	Untracked  int      `json:"untracked"`
	Conflicts  int      `json:"conflicts"`
	Files      int      `json:"files"`
	Branch     string   `json:"branch"`
	Operations []string `json:"operations"`
	CheckedAt  float64  `json:"checked_at"`
}

type branch struct {
	Name        string `json:"name"`
	OID         string `json:"oid"`
	Destination string `json:"destination"`
	Remote      string `json:"remote"`
	Ahead       int    `json:"ahead"`
	Error       string `json:"error"`
}

type repository struct {
	ID        string     `json:"id"`
	Path      string     `json:"path"`
	Name      string     `json:"name"`
	Errors    []string   `json:"errors"`
	Worktrees []worktree `json:"worktrees"`
	Branches  []branch   `json:"branches"`
	Stashes   int        `json:"stashes"`
	CheckedAt float64    `json:"checked_at"`
}

func branches(ctx context.Context, path string) ([]branch, error) {
	out, err := git(ctx, path, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads")
	if err != nil {
		return nil, err
	}
	var result []branch
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		ref, oid, ok := strings.Cut(line, "\x00")
		if !ok {
			return nil, errors.New("invalid Git branch output")
		}
		b := branch{Name: strings.TrimPrefix(ref, "refs/heads/"), OID: oid}
		destination, err := git(ctx, path, "rev-parse", "--symbolic-full-name", b.Name+"@{push}")
		if err == nil {
			remote, err := git(ctx, path, "for-each-ref", "--format=%(push:remotename)", ref)
			if err == nil {
				b.Destination, b.Remote = strings.TrimSpace(destination), strings.TrimSpace(remote)
			}
		}
		result = append(result, b)
	}
	return result, nil
}

func parseStatus(out string) (worktree, error) {
	var tree worktree
	skipOriginalPath := false
	for entry := range strings.SplitSeq(out, "\x00") {
		if skipOriginalPath {
			skipOriginalPath = false
			continue
		}
		if entry == "" {
			continue
		}
		switch entry[0] {
		case '1', '2':
			fields := strings.SplitN(entry, " ", 3)
			if len(fields) != 3 || len(fields[1]) != 2 {
				return tree, errors.New("invalid Git status output")
			}
			if fields[1][0] != '.' {
				tree.Staged++
			}
			if fields[1][1] != '.' {
				tree.Unstaged++
			}
			tree.Files++
			skipOriginalPath = entry[0] == '2'
		case 'u':
			tree.Conflicts++
			tree.Files++
		case '?':
			tree.Untracked++
			tree.Files++
		}
	}
	return tree, nil
}

func worktreeStatus(ctx context.Context, path string) (worktree, error) {
	out, err := git(ctx, path, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return worktree{}, err
	}
	tree, err := parseStatus(out)
	if err != nil {
		return tree, err
	}
	tree.Path = path
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "BISECT_LOG"} {
		location, err := git(ctx, path, "rev-parse", "--path-format=absolute", "--git-path", marker)
		if err != nil {
			return tree, err
		}
		if _, err := os.Stat(strings.TrimSpace(location)); err == nil {
			tree.Operations = append(tree.Operations, marker)
		} else if !errors.Is(err, os.ErrNotExist) {
			return tree, err
		}
	}
	name, err := git(ctx, path, "symbolic-ref", "--short", "HEAD")
	tree.Branch = strings.TrimSpace(name)
	if err != nil {
		tree.Branch = "DETACHED HEAD"
	}
	tree.CheckedAt = nowSeconds()
	return tree, nil
}

func inspectRepo(ctx context.Context, path string) (repo repository) {
	defer func() { repo.CheckedAt = nowSeconds() }()
	absolute, err := filepath.Abs(path)
	if err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	repo = repository{ID: path, Path: path, Name: filepath.Base(path)}
	common, err := git(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		repo.Errors = append(repo.Errors, err.Error())
		return repo
	}
	repo.ID = strings.TrimSpace(common)
	bare, err := git(ctx, path, "rev-parse", "--is-bare-repository")
	if err != nil {
		repo.Errors = append(repo.Errors, err.Error())
		return repo
	}
	if strings.TrimSpace(bare) == "true" {
		repo.Errors = append(repo.Errors, "bare repository has no working tree")
		return repo
	}
	out, err := git(ctx, path, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		repo.Errors = append(repo.Errors, err.Error())
		return repo
	}
	for record := range strings.SplitSeq(out, "\x00\x00") {
		first, _, _ := strings.Cut(record, "\x00")
		location, ok := strings.CutPrefix(first, "worktree ")
		if !ok {
			continue
		}
		tree, err := worktreeStatus(ctx, location)
		if err != nil {
			repo.Errors = append(repo.Errors, fmt.Sprintf("worktree %s: %v", safe(location), err))
			continue
		}
		repo.Worktrees = append(repo.Worktrees, tree)
	}
	out, err = git(ctx, path, "stash", "list", "--format=%H")
	if err != nil {
		repo.Errors = append(repo.Errors, err.Error())
		return repo
	}
	repo.Stashes = len(strings.Fields(out))
	repo.Branches, err = branches(ctx, path)
	if err != nil {
		repo.Errors = append(repo.Errors, err.Error())
		return repo
	}
	for i := range repo.Branches {
		b := &repo.Branches[i]
		if !strings.HasPrefix(b.Destination, "refs/remotes/") || b.Remote == "" {
			b.Error = "no configured push destination; publication cannot be verified"
			continue
		}
		count, err := git(ctx, path, "rev-list", "--count", b.Destination+".."+b.OID)
		if err == nil {
			b.Ahead, err = strconv.Atoi(strings.TrimSpace(count))
		}
		if err != nil {
			b.Error = "publication destination missing or unreadable"
		}
	}
	if len(repo.Branches) == 0 {
		repo.Errors = append(repo.Errors, "no local branch with a publication destination")
	}
	return repo
}
