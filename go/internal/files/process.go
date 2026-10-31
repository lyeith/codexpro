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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, errout := &boundedBuffer{limit: limit, cancel: cancel}, &boundedBuffer{limit: limit, cancel: cancel}
	cmd.Stdout = out
	cmd.Stderr = errout
	done := make(chan struct{})
	var escalation sync.WaitGroup
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pid := cmd.Process.Pid
		e := syscall.Kill(-pid, syscall.SIGTERM)
		escalation.Add(1)
		go func() {
			defer escalation.Done()
			select {
			case <-done:
			case <-time.After(time.Second):
				select {
				case <-done:
					return
				default:
				}
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}()
		if e == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return e
	}
	cmd.WaitDelay = 1500 * time.Millisecond
	e := cmd.Start()
	if e == nil {
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10)
		e = cmd.Wait()
	}
	// WaitDelay may expire after the direct child has exited while a descendant
	// still owns an output pipe. A nonzero exit can hide ErrWaitDelay behind an
	// ExitError, so every unsuccessful wait drains this temporary process group.
	if cmd.Process != nil && (e != nil || ctx.Err() != nil) {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	close(done)
	// Cmd.Wait has joined its Cancel callback, so no further Add can race Wait.
	// Join the optional escalation as well: no delayed signal may outlive this call.
	escalation.Wait()
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
