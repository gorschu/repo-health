package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpAndVersion(t *testing.T) {
	info := BuildInfo{Version: "1.2.3", Commit: "abc123", Date: "2026-11-05T10:00:00Z"}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "Available Commands:"},
		{[]string{"--help"}, "completion"},
		{[]string{"help", "prompt"}, "--severity"},
		{[]string{"--version"}, "repo-health 1.2.3"},
		{[]string{"version"}, "commit: abc123"},
		{[]string{"completion", "zsh"}, "#compdef repo-health"},
	} {
		var out, errOut bytes.Buffer
		if code := Run(t.Context(), tc.args, &out, &errOut, info); code != 0 {
			t.Fatalf("%v: %d: %s", tc.args, code, &errOut)
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Fatalf("%v: %s", tc.args, &out)
		}
	}
	var out, errOut bytes.Buffer
	if code := Run(t.Context(), []string{"inspect", "--help"}, &out, &errOut, info); code != 0 || strings.Contains(out.String(), "--record") {
		t.Fatalf("internal worker flag leaked into help: %s", &out)
	}
}

func TestInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"unknown"}, {"scan", "--fetch"}, {"prompt", "--severity", "ok"}, {"show", "extra"}} {
		var out, errOut bytes.Buffer
		if code := Run(t.Context(), args, &out, &errOut, BuildInfo{}); code != 2 {
			t.Fatalf("%v: code %d", args, code)
		}
		if !strings.Contains(errOut.String(), "Error:") {
			t.Fatal("missing usage error")
		}
	}
}

func TestOperationalError(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	var out, errOut bytes.Buffer
	if code := Run(t.Context(), []string{"show"}, &out, &errOut, BuildInfo{}); code != 1 {
		t.Fatalf("code %d: %s", code, &errOut)
	}
	if !strings.Contains(errOut.String(), "Repo check failed:") {
		t.Fatal("missing operational error")
	}
}

func TestPromptWithMissingCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if code := Run(t.Context(), []string{"prompt"}, &out, &errOut, BuildInfo{}); code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(out.String(), "not checked yet") {
		t.Fatal(out.String())
	}
}
