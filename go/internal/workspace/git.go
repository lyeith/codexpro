package workspace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const worktreeGitTimeout = 180 * time.Second

func (m *Manager) runGit(ctx context.Context, root string, args ...string) (string, error) {
	commandContext, cancel := context.WithTimeout(ctx, worktreeGitTimeout)
	defer cancel()
	argv := append([]string{"-n", "10", "git"}, args...)
	cmd := exec.CommandContext(commandContext, "nice", argv...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "GIT_TERMINAL_PROMPT=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 1500 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	var out, errout bytes.Buffer
	var overflow atomic.Bool
	limit := max(0, m.cfg.Int("maxOutputBytes", 120000))
	// The baseline caps each stream separately. Keep that public contract and
	// cancel as soon as either cap is crossed, rather than waiting for Git to exit.
	cmd.Stdout = &gitOutputWriter{buffer: &out, limit: limit, overflow: &overflow, cancel: cancel}
	cmd.Stderr = &gitOutputWriter{buffer: &errout, limit: limit, overflow: &overflow, cancel: cancel}
	err := cmd.Run()
	// WaitDelay can expire after the direct process exits while descendants keep
	// its pipes open. That path has no context error: it must still drain the
	// owned process group. Cancellation/overflow need the same final escalation.
	// A nonzero direct exit can mask ErrWaitDelay, so every unsuccessful wait
	// must also release the temporary group before returning its original error.
	if cmd.Process != nil && (err != nil || commandContext.Err() != nil || overflow.Load()) {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if overflow.Load() {
		return "", wire.Errorf("", "Git output exceeded %d bytes.", limit)
	}
	if commandContext.Err() != nil {
		return "", wire.Errorf("", "Git command timed out after %d ms.", worktreeGitTimeout.Milliseconds())
	}
	if err != nil {
		message := strings.TrimSpace(errout.String())
		if message == "" {
			message = strings.TrimSpace(out.String())
		}
		if message == "" {
			message = err.Error()
		}
		return "", wire.Errorf("", "%s", message)
	}
	return strings.TrimSpace(out.String()), nil
}

type gitOutputWriter struct {
	buffer   *bytes.Buffer
	limit    int
	overflow *atomic.Bool
	cancel   context.CancelFunc
}

func (w *gitOutputWriter) Write(p []byte) (int, error) {
	n := len(p)
	remaining := max(0, w.limit-w.buffer.Len())
	if n > remaining {
		if w.overflow.CompareAndSwap(false, true) {
			w.cancel()
		}
		p = p[:remaining]
	}
	_, _ = w.buffer.Write(p)
	return n, nil
}
