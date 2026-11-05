package health

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func runCommand(ctx context.Context, timeout time.Duration, env []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", errors.New("command timed out")
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		// Git stderr can contain remote URLs or credentials; don't cache it.
		return "", fmt.Errorf("command failed (exit %d)", exit.ExitCode())
	}
	return string(out), err
}

func git(ctx context.Context, path string, args ...string) (string, error) {
	env := []string{"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_NO_LAZY_FETCH=1"}
	// Partial clones must report missing objects instead of fetching implicitly.
	args = append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", path}, args...)
	return runCommand(ctx, 10*time.Second, env, "git", args...)
}
