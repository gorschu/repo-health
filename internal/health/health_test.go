package health

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

var testBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "repo-health-go-tests-")
	if err != nil {
		panic(err)
	}
	testBinary = filepath.Join(dir, "repo-health")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", testBinary, "../../cmd/repo-health")
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		panic(string(out) + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type fixture struct{ root, repo, remote string }

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	for key, value := range map[string]string{
		"HOME": root, "XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_ALLOW_PROTOCOL": "file",
		"GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.invalid",
		"GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.invalid",
	} {
		t.Setenv(key, value)
	}
	f := fixture{root, filepath.Join(root, "project"), filepath.Join(root, "remote.git")}
	f.git(t, root, "init", "--bare", f.remote)
	f.git(t, root, "init", "-b", "main", f.repo)
	writeTestFile(t, filepath.Join(f.repo, "tracked"), "initial\n")
	f.git(t, f.repo, "add", ".")
	f.git(t, f.repo, "commit", "-m", "initial")
	f.git(t, f.repo, "remote", "add", "origin", f.remote)
	f.git(t, f.repo, "push", "-u", "origin", "main")
	return f
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) git(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", path}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return string(out)
}

type findings struct{ trees, staged, unstaged, untracked, conflicts, files, stashes, ahead, branchIssues, operations, detached int }

func TestGitInspection(t *testing.T) {
	cases := []struct {
		name  string
		want  findings
		setup func(*testing.T, fixture)
	}{
		{"clean", findings{trees: 1}, func(t *testing.T, f fixture) {}},
		{"staged only", findings{trees: 1, staged: 1, files: 1}, func(t *testing.T, f fixture) {
			writeTestFile(t, filepath.Join(f.repo, "tracked"), "staged\n")
			f.git(t, f.repo, "add", "tracked")
		}},
		{"split index and unusual filenames", findings{trees: 1, staged: 1, unstaged: 1, untracked: 1, files: 2}, func(t *testing.T, f fixture) {
			writeTestFile(t, filepath.Join(f.repo, "tracked"), "staged\n")
			f.git(t, f.repo, "add", "tracked")
			writeTestFile(t, filepath.Join(f.repo, "tracked"), "unstaged\n")
			writeTestFile(t, filepath.Join(f.repo, "odd\nname\t.bin"), "\x00\xff\x01")
		}},
		{"rename", findings{trees: 1, staged: 1, files: 1}, func(t *testing.T, f fixture) { f.git(t, f.repo, "mv", "tracked", "renamed\nfile") }},
		{"linked worktree and stash", findings{trees: 2, stashes: 1, untracked: 1, files: 1, detached: 1}, func(t *testing.T, f fixture) {
			linked := filepath.Join(f.root, "linked")
			f.git(t, f.repo, "worktree", "add", "--detach", linked)
			writeTestFile(t, filepath.Join(linked, "unsaved"), "work\n")
			writeTestFile(t, filepath.Join(f.repo, "tracked"), "stash\n")
			f.git(t, f.repo, "stash", "push")
		}},
		{"noncurrent unpublished commit", findings{trees: 1, ahead: 1}, func(t *testing.T, f fixture) {
			f.git(t, f.repo, "checkout", "-b", "feature")
			f.git(t, f.repo, "push", "-u", "origin", "feature")
			writeTestFile(t, filepath.Join(f.repo, "tracked"), "feature\n")
			f.git(t, f.repo, "commit", "-am", "feature")
			f.git(t, f.repo, "checkout", "main")
		}},
		{"missing push target", findings{trees: 1, branchIssues: 1}, func(t *testing.T, f fixture) { f.git(t, f.repo, "branch", "unpublished") }},
		{"deleted tracking ref", findings{trees: 1, branchIssues: 1}, func(t *testing.T, f fixture) { f.git(t, f.repo, "update-ref", "-d", "refs/remotes/origin/main") }},
		{"unfinished merge", findings{trees: 1, conflicts: 1, files: 1, ahead: 1, branchIssues: 1, operations: 1}, func(t *testing.T, f fixture) {
			f.git(t, f.repo, "checkout", "-b", "feature")
			writeTestFile(t, filepath.Join(f.repo, "tracked"), "feature\n")
			f.git(t, f.repo, "commit", "-am", "feature")
			f.git(t, f.repo, "checkout", "main")
			writeTestFile(t, filepath.Join(f.repo, "tracked"), "main\n")
			f.git(t, f.repo, "commit", "-am", "main")
			if exec.CommandContext(t.Context(), "git", "-C", f.repo, "merge", "feature").Run() == nil {
				t.Fatal("expected a conflicting merge")
			}
		}},
		{"triangular push", findings{trees: 1, ahead: 1}, func(t *testing.T, f fixture) {
			fork := filepath.Join(f.root, "fork.git")
			f.git(t, f.root, "init", "--bare", fork)
			f.git(t, f.repo, "remote", "add", "fork", fork)
			f.git(t, f.repo, "push", "fork", "main")
			f.git(t, f.repo, "config", "remote.pushDefault", "fork")
			f.git(t, f.repo, "config", "push.default", "current")
			writeTestFile(t, filepath.Join(f.repo, "tracked"), "only upstream\n")
			f.git(t, f.repo, "commit", "-am", "upstream only")
			f.git(t, f.repo, "push", "origin", "main")
		}},
		{"unavailable remote", findings{trees: 1}, func(t *testing.T, f fixture) {
			if err := os.Rename(f.remote, filepath.Join(f.root, "offline.git")); err != nil {
				t.Fatal(err)
			}
		}},
		{"incoming commits", findings{trees: 1}, func(t *testing.T, f fixture) {
			other := filepath.Join(f.root, "other")
			f.git(t, f.root, "clone", "-b", "main", f.remote, other)
			writeTestFile(t, filepath.Join(other, "tracked"), "incoming\n")
			f.git(t, other, "commit", "-am", "incoming")
			f.git(t, other, "push")
			f.git(t, f.repo, "fetch", "origin")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.setup(t, f)
			beforeStatus := f.git(t, f.repo, "status", "--porcelain=v2", "-z")
			beforeIndex, err := os.ReadFile(filepath.Join(f.repo, ".git/index"))
			if err != nil {
				t.Fatal(err)
			}
			beforeRefs := f.git(t, f.repo, "show-ref")
			repo := inspectRepo(t.Context(), f.repo)
			if len(repo.Errors) != 0 {
				t.Fatalf("inspection failed: %v", repo.Errors)
			}
			got := findings{trees: len(repo.Worktrees), stashes: repo.Stashes}
			for _, tree := range repo.Worktrees {
				got.staged += tree.Staged
				got.unstaged += tree.Unstaged
				got.untracked += tree.Untracked
				got.conflicts += tree.Conflicts
				got.files += tree.Files
				got.operations += len(tree.Operations)
				if tree.Branch == "DETACHED HEAD" {
					got.detached++
				}
			}
			for _, b := range repo.Branches {
				got.ahead += b.Ahead
				if b.Error != "" {
					got.branchIssues++
				}
			}
			if got != tc.want {
				t.Fatalf("got %+v; want %+v", got, tc.want)
			}
			if tc.name == "triangular push" && repo.Branches[0].Remote != "fork" {
				t.Fatal("compared against the wrong destination")
			}
			if tc.want == (findings{trees: 1}) {
				data := report{Schema: schema, CheckedAt: nowSeconds(), Repos: []repository{repo}}
				if text := display(data, "prompt", "", nowSeconds()); text != "" {
					t.Fatal(text)
				}
			}
			afterIndex, err := os.ReadFile(filepath.Join(f.repo, ".git/index"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(beforeIndex, afterIndex) || beforeRefs != f.git(t, f.repo, "show-ref") || beforeStatus != f.git(t, f.repo, "status", "--porcelain=v2", "-z") {
				t.Fatal("inspection changed the index, refs or worktree state")
			}
		})
	}
}

func TestCachedDisplay(t *testing.T) {
	f := newFixture(t)
	writeTestFile(t, filepath.Join(f.repo, "tracked"), "stash\n")
	f.git(t, f.repo, "stash", "push")
	f.git(t, f.repo, "branch", "local-only")
	repo := inspectRepo(t.Context(), f.repo)
	data := report{Schema: schema, CheckedAt: nowSeconds(), Repos: []repository{repo}}
	dir, err := cacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "status.json"), data); err != nil {
		t.Fatal(err)
	}
	now := data.CheckedAt + 146
	wantPrompt := " Local work: 1 repo · 1 stash · 1 branch without a push target · checked 2m ago   mani run repo-check\n"
	if got := display(readReport(dir), "prompt", "", now); got != wantPrompt {
		t.Fatalf("got %q; want %q", got, wantPrompt)
	}
	wantSummary := "1 repo · 1 stash · 1 branch without a push target · checked 2m ago\n"
	if got := display(readReport(dir), "summary", "", now); got != wantSummary {
		t.Fatalf("summary must stay on one line: got %q; want %q", got, wantSummary)
	}
	got := display(readReport(dir), "show", "", now)
	for _, want := range []string{"Local work: 1 repo · 1 stash", "checked 2m ago", f.repo, "local-only:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("detailed report missing %q: %s", want, got)
		}
	}
	if !strings.Contains(display(data, "prompt", "error", nowSeconds()), "1 stash") {
		t.Fatal("stash missing")
	}
	if display(data, "prompt", "warning", nowSeconds()) != "" {
		t.Fatal("duplicate severity output")
	}
	info, err := os.Stat(filepath.Join(dir, "status.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache must be private: %v", err)
	}
}

func TestCheckAge(t *testing.T) {
	const checkedAt = 100000.0
	for _, tc := range []struct {
		age  float64
		want string
	}{
		{-1, "check time in future"},
		{0, "checked 0s ago"},
		{59.9, "checked 59s ago"},
		{60, "checked 1m ago"},
		{146, "checked 2m ago"},
		{3599, "checked 59m ago"},
		{3600, "checked 1h ago"},
		{86399, "checked 23h ago"},
		{86400, "checked 1d ago"},
	} {
		if got := checkAge(checkedAt, checkedAt+tc.age); got != tc.want {
			t.Errorf("age %v: got %q; want %q", tc.age, got, tc.want)
		}
	}
}

func TestDisplayAgeWarnings(t *testing.T) {
	const now = 100000.0
	fresh := report{Schema: schema, CheckedAt: now - 146}
	if got := display(fresh, "prompt", "", now); got != "" {
		t.Fatalf("clean prompt must stay silent: %q", got)
	}
	if got := display(fresh, "summary", "", now); !strings.Contains(got, "checked 2m ago") || strings.Count(got, "\n") != 1 {
		t.Fatalf("clean summary must include its age on one line: %q", got)
	}
	for _, mode := range []string{"prompt", "summary"} {
		old := report{Schema: schema, CheckedAt: now - 601}
		if got := display(old, mode, "", now); !strings.Contains(got, "stale") || !strings.Contains(got, "checked 10m ago") {
			t.Fatalf("stale report must include age and warning: %q", got)
		}
		future := report{Schema: schema, CheckedAt: now + 60}
		if got := display(future, mode, "", now); !strings.Contains(got, "stale") || !strings.Contains(got, "check time in future") {
			t.Fatalf("clock skew must not look fresh: %q", got)
		}
		if got := display(report{}, mode, "", now); !strings.Contains(got, "not checked yet") || strings.Contains(got, "ago") {
			t.Fatalf("missing cache must not claim a check age: %q", got)
		}
	}
}

func TestCacheWarnings(t *testing.T) {
	f := newFixture(t)
	repo := inspectRepo(t.Context(), f.repo)
	repo.Stashes = 1
	data := report{Schema: schema, CheckedAt: nowSeconds() - 601, Repos: []repository{repo}}
	if got := display(data, "prompt", "error", nowSeconds()); !strings.Contains(got, "1 stash") || !strings.Contains(got, "stale") {
		t.Fatal(got)
	}
	dir, err := cacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, broken := range []string{"not JSON", `{"schema":1}`, `{"schema":2,"schema":2}`} {
		writeTestFile(t, filepath.Join(dir, "status.json"), broken)
		if got := display(readReport(dir), "prompt", "", nowSeconds()); !strings.Contains(got, "not checked yet") {
			t.Fatal(got)
		}
	}
}

func TestPartialCloneStaysOffline(t *testing.T) {
	f := newFixture(t)
	f.git(t, f.remote, "config", "uploadpack.allowFilter", "true")
	partial := filepath.Join(f.root, "partial")
	blob := strings.TrimSpace(f.git(t, f.repo, "rev-parse", "HEAD:tracked"))
	f.git(t, f.root, "clone", "--no-checkout", "--filter=blob:none", "-b", "main", "file://"+f.remote, partial)
	if _, err := git(t.Context(), partial, "cat-file", "-p", blob); err == nil {
		t.Fatal("missing object fetched implicitly")
	}
	if got := f.git(t, partial, "cat-file", "-p", blob); got != "initial\n" {
		t.Fatal(got)
	}
}

func TestExternalFetchUpdatesNextCheck(t *testing.T) {
	f := newFixture(t)
	writeTestFile(t, filepath.Join(f.repo, "tracked"), "published elsewhere\n")
	f.git(t, f.repo, "commit", "-am", "work")
	other := filepath.Join(f.root, "other")
	f.git(t, f.root, "clone", f.repo, other)
	f.git(t, other, "push", f.remote, "main")
	if got := inspectRepo(t.Context(), f.repo).Branches[0].Ahead; got != 1 {
		t.Fatal(got)
	}
	f.git(t, f.repo, "fetch", "origin")
	if got := inspectRepo(t.Context(), f.repo).Branches[0].Ahead; got != 0 {
		t.Fatal(got)
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "should-not-exist")
	started := time.Now()
	_, err := runCommand(t.Context(), 50*time.Millisecond, []string{"MARKER=" + marker}, "sh", "-c", `(sleep 0.2; touch "$MARKER") & wait`)
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(started) > 2*time.Second {
		t.Fatalf("timeout: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("descendant survived timeout")
	}
}

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runCommand(ctx, time.Second, nil, "sleep", "1"); err == nil {
		t.Fatal("canceled command ran")
	}
}

func TestManiScanOfflineAndRecovery(t *testing.T) {
	if _, err := exec.LookPath("mani"); err != nil {
		t.Skip("Mani required for collector integration")
	}
	f := newFixture(t)
	config := filepath.Join(f.root, "mani.yaml")
	pathJSON, err := json.Marshal(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, config, "projects:\n  test:\n    path: "+string(pathJSON)+"\n")
	t.Setenv("MANI_CONFIG", config)
	marker := filepath.Join(f.root, "ssh-called")
	ssh := filepath.Join(f.root, "ssh")
	writeTestFile(t, ssh, "#!/bin/sh\ntouch \"$REPO_HEALTH_TEST_MARKER\"\nexit 1\n")
	if err := os.Chmod(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", shellQuote(ssh))
	t.Setenv("GIT_ALLOW_PROTOCOL", "ssh")
	t.Setenv("REPO_HEALTH_TEST_MARKER", marker)
	f.git(t, f.repo, "remote", "set-url", "origin", "ssh://git@example.invalid/project")
	writeTestFile(t, filepath.Join(f.repo, "local"), "work\n")
	// Exercise the command path quoting used when Mani launches workers.
	binary := filepath.Join(f.root, "repo health's binary")
	contents, err := os.ReadFile(testBinary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, contents, 0o700); err != nil {
		t.Fatal(err)
	}
	runScan := func(wantSuccess bool) {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), binary, "scan").CombinedOutput()
		if (err == nil) != wantSuccess {
			t.Fatalf("scan: %v: %s", err, out)
		}
	}
	runScan(true)
	dir, err := cacheDir()
	if err != nil {
		t.Fatal(err)
	}
	data := readReport(dir)
	if len(data.Repos) != 1 || len(data.Errors) != 0 || len(issues(data.Repos[0], nowSeconds())) != 0 {
		t.Fatalf("%+v", data)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("scanner invoked SSH")
	}
	if !strings.Contains(display(data, "prompt", "", nowSeconds()), "Local work") {
		t.Fatal("lost local work")
	}
	// A concurrent invocation leaves the published report untouched.
	lock, err := os.OpenFile(filepath.Join(dir, "scan.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	runScan(true)
	if readReport(dir).CheckedAt != data.CheckedAt {
		t.Fatal("concurrent scan overwrote report")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, config, "projects:\n  absent:\n    path: "+f.root+"/absent\n")
	runScan(false)
	data = readReport(dir)
	if len(data.Repos) != 1 || len(data.Errors) == 0 {
		t.Fatal("missing project erased old findings")
	}
	if !strings.Contains(display(data, "prompt", "", nowSeconds()), "scan incomplete") {
		t.Fatal("missing failure warning")
	}
}

func BenchmarkCachedPrompt(b *testing.B) {
	now := nowSeconds()
	data := report{Schema: schema, CheckedAt: now, Repos: []repository{{CheckedAt: now, Stashes: 1}}}
	for b.Loop() {
		display(data, "prompt", "", now)
	}
}
