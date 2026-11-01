package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

var claimIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Allocation struct {
	RunID          string            `json:"run_id"`
	RegistrationID string            `json:"registration_id"`
	Scratch        string            `json:"scratch"`
	Environment    map[string]string `json:"environment"`
	Owner          map[string]any    `json:"owner"`
}

type Settlement struct {
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	Quiescent bool   `json:"quiescent"`
	Cancelled bool   `json:"cancelled"`
}

type limitedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.limit - b.Buffer.Len()
	if len(p) > remaining {
		b.overflow = true
		p = p[:max(0, remaining)]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

// RunBounded isolates each short control command so timeouts cannot strand its children.
func RunBounded(ctx context.Context, timeout time.Duration, maxBytes int, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Recovery helpers can perform substantial host bookkeeping. Their CPU
	// scheduling priority must not displace the HTTP/control-plane process.
	cmd := exec.CommandContext(ctx, "nice", append([]string{"-n", "10", name}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 250 * time.Millisecond
	out := &limitedBuffer{limit: maxBytes}
	cmd.Stdout = out
	cmd.Stderr = &limitedBuffer{limit: 4096}
	err := cmd.Run()
	// A direct helper can exit while its descendant still owns an output
	// pipe. WaitDelay then returns an error without invoking Cancel; a
	// nonzero exit can also hide that delay behind ExitError. Drain only
	// this command's temporary group before returning either failure.
	if cmd.Process != nil && (err != nil || ctx.Err() != nil) {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil {
		return nil, err
	}
	if out.overflow {
		return nil, errors.New("control response exceeds its byte limit")
	}
	return append([]byte(nil), out.Bytes()...), nil
}

func ValidateClaim(c ResourceClaim) error {
	if runtime.GOOS != "linux" || !filepath.IsAbs(c.Helper) {
		return errors.New("Host job resources require an absolute helper path on Linux.")
	}
	if c.Version != 1 || !claimIDPattern.MatchString(c.RunID) || !claimIDPattern.MatchString(c.RegistrationID) {
		return errors.New("Host job resource registration was invalid.")
	}
	return nil
}

// ErrHelperFailed is a host-resource helper invocation that exited non-zero
// or did not finish within its 10 s cap, as happens while ssd-dev maintenance
// (ssd-dev-gc) holds the helper's cache lock.
var ErrHelperFailed = errors.New("Host job resource operation failed.")

func invokeResource(ctx context.Context, c ResourceClaim, args []string, out any) error {
	if err := ValidateClaim(c); err != nil {
		return err
	}
	b, err := RunBounded(ctx, 10*time.Second, 1024*1024, c.Helper, append([]string{"external"}, args...)...)
	if err != nil {
		return ErrHelperFailed
	}
	if err = json.Unmarshal(b, out); err != nil {
		return errors.New("Host job resource response was invalid.")
	}
	return nil
}

func BeginResources(ctx context.Context, c ResourceClaim, cwd, unit string) (Allocation, error) {
	var a Allocation
	err := invokeResource(ctx, c, []string{"begin", "--purpose", "tool", "--cwd", cwd, "--owner-unit", unit, "--cache-consumer", "--run-id", c.RunID, "--registration-id", c.RegistrationID}, &a)
	if err == nil && (a.RunID != c.RunID || a.RegistrationID != c.RegistrationID) {
		err = errors.New("Host job resource registration was invalid.")
	}
	return a, err
}

// BeginResourcesWithRetry retries an allocation that failed in the helper for
// up to budget, with backoff, before reporting a retryable error. A begin that
// was blocked on the helper's lock allocated nothing, so retrying with the same
// identifiers is safe; a duplicate allocation fails and is cancelled by the
// caller's normal revocation of the claim. No command has started either way.
func BeginResourcesWithRetry(ctx context.Context, c ResourceClaim, cwd, unit string, budget time.Duration) (Allocation, error) {
	deadline := time.Now().Add(budget)
	wait := time.Second
	for {
		a, err := BeginResources(ctx, c, cwd, unit)
		if err == nil || !errors.Is(err, ErrHelperFailed) {
			return a, err
		}
		if time.Now().Add(wait).After(deadline) {
			return a, ErrHostResourcesUnavailable
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return a, ctx.Err()
		case <-timer.C:
		}
		wait = min(2*wait, 5*time.Second)
	}
}

// ErrHostResourcesUnavailable is returned to the tool caller when allocation
// kept failing for the whole retry budget. Nothing was started, so the same
// call can be retried unchanged.
var ErrHostResourcesUnavailable = &wire.Error{Code: "host_resources_unavailable", Message: "Host job resources are temporarily unavailable (SSD maintenance may be running). No command was started; retry the same call shortly.", Data: map[string]any{"retry_unchanged": true}}

func ResourceCommand(c ResourceClaim, command []string) (string, []string) {
	return c.Helper, append([]string{"external", "exec", c.RunID, "--registration-id", c.RegistrationID, "--"}, command...)
}

func FinishResources(ctx context.Context, c ResourceClaim, exit int) (Settlement, error) {
	var s Settlement
	if exit < 0 || exit > 255 {
		exit = 125
	}
	err := invokeResource(ctx, c, []string{"finish", c.RunID, "--registration-id", c.RegistrationID, "--exit-code", itoa(exit)}, &s)
	if err == nil && (s.RunID != c.RunID || !s.Quiescent) {
		err = errors.New("Host job resource ownership remains unresolved.")
	}
	return s, err
}

func CancelResources(ctx context.Context, c ResourceClaim) (Settlement, error) {
	var s Settlement
	err := invokeResource(ctx, c, []string{"cancel", c.RunID, "--registration-id", c.RegistrationID}, &s)
	if err == nil && (s.RunID != c.RunID || !s.Quiescent || !s.Cancelled) {
		err = errors.New("Host job resource cancellation was not verified.")
	}
	return s, err
}
