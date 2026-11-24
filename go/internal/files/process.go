package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
)

type ProcessResult struct {
	Stdout, Stderr      string
	ExitCode            int
	Signal              string
	Err                 error
	TimedOut, Truncated bool
}
type boundedBuffer struct {
	mu       sync.Mutex
	b        bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := b.limit - b.b.Len()
	if left > 0 {
		b.b.Write(p[:min(left, n)])
	}
	if n > left {
		b.overflow = true
		b.cancel()
	}
	return n, nil
}
func (b *boundedBuffer) result() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return DecodeText(b.b.Bytes()), b.overflow
}

// RunCommand bounds both streams and owns the temporary process group. It never
// touches persistent jobs, which are owned by the separate jobs service.
func RunCommand(ctx context.Context, root, name string, args []string, input string, limit int, timeout time.Duration) ProcessResult {
	return RunCommandEnv(ctx, root, name, args, input, limit, timeout, nil)
}

// RunCommandEnv applies explicit child-only environment overrides.
func RunCommandEnv(ctx context.Context, root, name string, args []string, input string, limit int, timeout time.Duration, environment []string) ProcessResult {
	if limit < 1 {
		limit = 1
	}
	parent := ctx
	var stop context.CancelFunc
	if timeout > 0 {
		ctx, stop = context.WithTimeout(ctx, timeout)
		defer stop()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = root
	cmd.Env = append(append(os.Environ(), "NO_COLOR=1"), environment...)
	cmd.Stdin = strings.NewReader(input)
	out, errout := &boundedBuffer{limit: limit, cancel: cancel}, &boundedBuffer{limit: limit, cancel: cancel}
	cmd.Stdout = out
	cmd.Stderr = errout
	e := process.RunTemporaryCommand(ctx, cmd)
	stdout, oc := out.result()
	stderr, ec := errout.result()
	r := ProcessResult{Stdout: stdout, Stderr: stderr, ExitCode: -1, Err: e, Truncated: oc || ec}
	if cmd.ProcessState != nil {
		r.ExitCode = cmd.ProcessState.ExitCode()
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			r.Signal = status.Signal().String()
		}
	}
	r.TimedOut = ctx.Err() == context.DeadlineExceeded && parent.Err() == nil
	var exitError *exec.ExitError
	if errors.As(r.Err, &exitError) && parent.Err() == nil && !r.TimedOut {
		r.Err = nil
	}
	if r.Truncated {
		r.Err = fmt.Errorf("spawnSync %s ENOBUFS", name)
	} else if r.TimedOut {
		r.Err = fmt.Errorf("spawnSync %s ETIMEDOUT", name)
	} else if e != nil && cmd.ProcessState == nil {
		if _, ok := e.(*exec.Error); ok {
			r.Err = fmt.Errorf("spawnSync %s ENOENT", name)
		}
	}
	return r
}
