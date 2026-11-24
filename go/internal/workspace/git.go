package workspace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

const workspaceGitTimeout = 180 * time.Second

func (m *Manager) runGit(ctx context.Context, root string, args ...string) (string, error) {
	commandContext, cancel := context.WithTimeout(ctx, workspaceGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(commandContext, "git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "GIT_TERMINAL_PROMPT=0")
	var out, errout bytes.Buffer
	var overflow atomic.Bool
	limit := max(0, m.cfg.Int("maxOutputBytes", 120000))
	// The baseline caps each stream separately. Keep that public contract and
	// cancel as soon as either cap is crossed, rather than waiting for Git to exit.
	cmd.Stdout = &gitOutputWriter{buffer: &out, limit: limit, overflow: &overflow, cancel: cancel}
	cmd.Stderr = &gitOutputWriter{buffer: &errout, limit: limit, overflow: &overflow, cancel: cancel}
	err := process.RunTemporaryCommand(commandContext, cmd)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if overflow.Load() {
		return "", wire.Errorf("", "Git output exceeded %d bytes.", limit)
	}
	if commandContext.Err() != nil {
		return "", wire.Errorf("", "Git command timed out after %d ms.", workspaceGitTimeout.Milliseconds())
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
