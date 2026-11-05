package main

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
	build := exec.Command("go", "build", "-buildvcs=false", "-o", testBinary, ".")
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

func normalize(repo repository) repository {
	repo.CheckedAt = 0
	for i := range repo.Worktrees {
		repo.Worktrees[i].CheckedAt = 0
	}
	return repo
}

func pythonRepo(t *testing.T, path string) repository {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python is required only for comparison with the reference implementation")
	}
	code := `import importlib.util, json, sys
spec = importlib.util.spec_from_file_location("reference", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
print(json.dumps(module.inspect_repo(sys.argv[2])))`
	cmd := exec.CommandContext(t.Context(), "python3", "-B", "-c", code, "reference/repo-health.py", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Python reference: %v: %s", err, out)
	}
	var result repository
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestGitParity(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*testing.T, fixture)
	}{
		{"clean", func(t *testing.T, f fixture) {}},
		{"staged only", func(t *testing.T, f fixture) {
			writeTestFile(t, filepath.Join(f.repo, "tracked"), "staged\n")
			f.git(t, f.repo, "add", "tracked")
		}},
		{"split index and unusual filenames", func(t *testing.T, f fixture) {
			writeTestFile(t, filepath.Join(f.repo, "tracked"), "staged\n")
			f.git(t, f.repo, "add", "tracked")
			writeTestFile(t, filepath.Join(f.repo, "tracked"), "unstaged\n")
			writeTestFile(t, filepath.Join(f.repo, "odd\nname\t.bin"), "\x00\xff\x01")
		}},
		{"rename", func(t *testing.T, f fixture) { f.git(t, f.repo, "mv", "tracked", "renamed\nfile") }},
		{"linked worktree and stash", func(t *testing.T, f fixture) {
			linked := filepath.Join(f.root, "linked")
			f.git(t, f.repo, "worktree", "add", "--detach", linked)
			writeTestFile(t, filepath.Join(linked, "unsaved"), "work\n")
			writeTestFile(t, filepath.Join(f.repo, "tracked"), "stash\n")
			f.git(t, f.repo, "stash", "push")
		}},
		{"noncurrent unpublished commit", func(t *testing.T, f fixture) {
			f.git(t, f.repo, "checkout", "-b", "feature")
			f.git(t, f.repo, "push", "-u", "origin", "feature")
			writeTestFile(t, filepath.Join(f.repo, "tracked"), "feature\n")
			f.git(t, f.repo, "commit", "-am", "feature")
			f.git(t, f.repo, "checkout", "main")
		}},
		{"missing push target", func(t *testing.T, f fixture) { f.git(t, f.repo, "branch", "unpublished") }},
		{"deleted tracking ref", func(t *testing.T, f fixture) { f.git(t, f.repo, "update-ref", "-d", "refs/remotes/origin/main") }},
		{"unfinished merge", func(t *testing.T, f fixture) {
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
		{"triangular push", func(t *testing.T, f fixture) {
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
		{"unavailable remote", func(t *testing.T, f fixture) {
			if err := os.Rename(f.remote, filepath.Join(f.root, "offline.git")); err != nil {
				t.Fatal(err)
			}
		}},
		{"incoming commits", func(t *testing.T, f fixture) {
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
			got := normalize(inspectRepo(t.Context(), f.repo))
			want := normalize(pythonRepo(t, f.repo))
			// Round-trip both to normalize nil versus empty slices for comparison.
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Fatalf("Go: %s\nPython: %s", gotJSON, wantJSON)
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

func TestDisplayParity(t *testing.T) {
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
	frozen := nowSeconds()
	clock, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"prompt", "summary", "show"} {
		code := `import importlib.util, sys
spec = importlib.util.spec_from_file_location("reference", "reference/repo-health.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
module.time.time = lambda: float(sys.argv[2])
module.display(sys.argv[1])`
		cmd := exec.CommandContext(t.Context(), "python3", "-B", "-c", code, mode, string(clock))
		want, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		got := display(data, mode, "", frozen)
		if got != string(want) {
			t.Fatalf("%s:\nGo: %s\nPython: %s", mode, got, want)
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
	t.Setenv("REPO_HEALTH_FETCH", "1") // The removed Python option must have no effect.
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

func TestCLIRejectsBadArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"scan", "--fetch"}, {"prompt", "--severity", "ok"}, {"show", "extra"}} {
		var out, errOut bytes.Buffer
		if code := runCLI(t.Context(), args, &out, &errOut); code != 2 {
			t.Fatalf("%v: code %d", args, code)
		}
	}
}

func BenchmarkCachedPrompt(b *testing.B) {
	now := nowSeconds()
	data := report{Schema: schema, CheckedAt: now, Repos: []repository{{CheckedAt: now, Stashes: 1}}}
	for b.Loop() {
		display(data, "prompt", "", now)
	}
}
