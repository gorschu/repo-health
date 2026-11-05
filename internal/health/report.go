package health

import (
	"cmp"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

const schema = 2
const localTTL = 600

type report struct {
	Schema    int          `json:"schema"`
	CheckedAt float64      `json:"checked_at"`
	Errors    []string     `json:"errors"`
	Repos     []repository `json:"repos"`
}

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func cacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "repo-health"), nil
}

func readReport(dir string) report {
	data, err := os.ReadFile(filepath.Join(dir, "status.json"))
	var result report
	if err != nil || json.Unmarshal(data, &result) != nil || result.Schema != schema {
		return report{}
	}
	return result
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".report-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func safe(value string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, value)
}

func number(count int, singular, plural string) string {
	label := singular
	if count != 1 {
		label = cmp.Or(plural, singular+"s")
	}
	return fmt.Sprintf("%d %s", count, label)
}

func stale(checkedAt, now float64) bool { return now-checkedAt > localTTL || now < checkedAt }

func issues(repo repository, now float64) []string {
	problems := slices.Clone(repo.Errors)
	if stale(repo.CheckedAt, now) {
		problems = append(problems, "local check stale")
	}
	for _, b := range repo.Branches {
		if b.Error != "" {
			problems = append(problems, b.Name+": "+b.Error)
		}
	}
	return problems
}

type repoTotals struct{ files, ahead, stashes, operations int }

func totals(repo repository) repoTotals {
	result := repoTotals{stashes: repo.Stashes}
	for _, tree := range repo.Worktrees {
		result.files += tree.Files
		if len(tree.Operations) > 0 || tree.Branch == "DETACHED HEAD" {
			result.operations++
		}
	}
	for _, b := range repo.Branches {
		if b.Ahead > 0 {
			result.ahead++
		}
	}
	return result
}

func checkMessages(repos []repository, now float64) []string {
	// Keep first-seen order stable in the prompt.
	type label struct{ singular, plural string }
	counts := make(map[label]int)
	var order []label
	add := func(singular, plural string) {
		key := label{singular, plural}
		if counts[key] == 0 {
			order = append(order, key)
		}
		counts[key]++
	}
	for _, repo := range repos {
		if len(repo.Errors) > 0 {
			add("repo check failed", "repo checks failed")
		}
		if stale(repo.CheckedAt, now) {
			add("repo check stale", "repo checks stale")
		}
		for _, b := range repo.Branches {
			if b.Error == "" {
				continue
			}
			if !strings.HasPrefix(b.Destination, "refs/remotes/") || b.Remote == "" {
				add("branch without a push target", "branches without push targets")
			} else {
				add("branch comparison failed", "branch comparisons failed")
			}
		}
	}
	var messages []string
	for _, key := range order {
		messages = append(messages, number(counts[key], key.singular, key.plural))
	}
	return messages
}

func describe(repo repository, now float64) string {
	var lines []string
	for _, tree := range repo.Worktrees {
		var parts []string
		for _, item := range []struct {
			count int
			label string
		}{{tree.Staged, "staged"}, {tree.Unstaged, "unstaged"}, {tree.Untracked, "untracked"}, {tree.Conflicts, "conflicts"}} {
			if item.count > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", item.count, item.label))
			}
		}
		if len(tree.Operations) > 0 {
			parts = append(parts, "operation in progress")
		}
		if tree.Branch == "DETACHED HEAD" {
			parts = append(parts, "detached HEAD")
		}
		if len(parts) > 0 {
			lines = append(lines, safe(tree.Path)+": "+strings.Join(parts, ", "))
		}
	}
	for _, b := range repo.Branches {
		if b.Ahead > 0 {
			lines = append(lines, fmt.Sprintf("%s: %d ahead of stored push target", safe(b.Name), b.Ahead))
		}
	}
	if repo.Stashes > 0 {
		lines = append(lines, number(repo.Stashes, "stash", "stashes"))
	}
	for _, problem := range issues(repo, now) {
		lines = append(lines, "Check needed: "+safe(problem))
	}
	return cmp.Or(strings.Join(lines, "\n"), "clean; no commits ahead of stored push targets")
}

func display(data report, mode, severity string, now float64) string {
	problems := slices.Clone(data.Errors)
	if data.Schema != schema {
		problems = append(problems, "not checked yet")
	} else if stale(data.CheckedAt, now) {
		problems = append(problems, "checker stale")
	}
	checks := checkMessages(data.Repos, now)
	var dirty, stashes, files, ahead int
	for _, repo := range data.Repos {
		v := totals(repo)
		if v.files > 0 || v.ahead > 0 || v.stashes > 0 || v.operations > 0 {
			dirty++
		}
		stashes += v.stashes
		files += v.files
		ahead += v.ahead
	}
	level := "ok"
	if len(problems) > 0 || len(checks) > 0 {
		level = "error"
	} else if dirty > 0 {
		level = "warning"
	}
	if severity != "" && level != severity {
		return ""
	}
	var pieces []string
	if dirty > 0 {
		pieces = append(pieces, "Local work: "+number(dirty, "repo", ""))
	}
	if stashes > 0 {
		pieces = append(pieces, number(stashes, "stash", "stashes"))
	}
	if mode != "prompt" {
		if files > 0 {
			pieces = append(pieces, number(files, "file", ""))
		}
		if ahead > 0 {
			pieces = append(pieces, number(ahead, "branch ahead", "branches ahead"))
		}
	}
	pieces = append(pieces, checks...)
	if len(problems) > 0 {
		message := "scan incomplete"
		if len(data.Errors) == 0 {
			message = safe(strings.Join(problems, "; "))
		}
		pieces = append(pieces, "Check needed: "+message)
	}
	if mode == "prompt" {
		if level == "ok" {
			return ""
		}
		return " " + strings.Join(pieces, " · ") + "   mani run repo-check\n"
	}
	var out strings.Builder
	fmt.Fprintln(&out, cmp.Or(strings.Join(pieces, " · "), number(len(data.Repos), "repo checked", "repos checked")+"; no outstanding work"))
	if data.CheckedAt != 0 {
		fmt.Fprintf(&out, "Local report: %ds ago\n", max(0, int(now-data.CheckedAt)))
	}
	if mode == "show" {
		for _, problem := range data.Errors {
			fmt.Fprintln(&out, "Check needed: "+safe(problem))
		}
		for _, repo := range data.Repos {
			fmt.Fprintf(&out, "\n%s (%s):\n%s\n", safe(repo.Name), safe(repo.Path), describe(repo, now))
		}
	}
	return out.String()
}
