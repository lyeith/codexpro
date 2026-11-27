package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

var claimIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Allocation struct {
	RunID          string               `json:"run_id"`
	RegistrationID string               `json:"registration_id"`
	Scratch        string               `json:"scratch"`
	Environment    map[string]string    `json:"environment"`
	Owner          map[string]any       `json:"owner"`
	UnitEnvelope   *GuardedUnitEnvelope `json:"unit_envelope,omitempty"`
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
	if (c.Version != 1 && c.Version != 2) || !claimIDPattern.MatchString(c.RunID) || !claimIDPattern.MatchString(c.RegistrationID) {
		return errors.New("Host job resource registration was invalid.")
	}
	if c.Version == 2 {
		return validateGuardedClaim(c)
	}
	if c.Config != "" || c.ReceiptsRoot != "" || c.DefaultHome != "" || len(c.ExecutionContext) != 0 || c.StopTimeoutUSec != 0 {
		return errors.New("Legacy resource claims cannot select guarded fields.")
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
	b, err := RunBounded(ctx, 10*time.Second, 1024*1024, c.Helper, resourceLifecycleArgs(c, args)...)
	if err != nil {
		return ErrHelperFailed
	}
	if _, settlement := out.(*Settlement); c.Version == 2 && settlement {
		if err := validateGuardedSettlement(b, c); err != nil {
			return err
		}
	}
	if err = decodeResourceInspection(b, out); err != nil {
		return errors.New("Host job resource response was invalid.")
	}
	return nil
}

// decodeResourceInspection fences ambiguous names before typed decoding. The
// helper response is byte bounded; use the JSON nesting limit and retain number
// tokens verbatim so uint64 owner identities never pass through float64.
func decodeResourceInspection(data []byte, out any) error {
	known := map[string][]string{
		"envelope": {"receipt", "pending", "alive", "quiescent", "allocation_claim", "unit_state"},
		"receipt":  {"run_id", "registration_id", "boot_id", "invocation_id", "status", "exit_code", "owner_quiescent_verified", "owner", "host_memory"},
		"owner":    {"kind", "unit", "invocation_id", "cgroup"},
		"cgroup":   {"path", "device", "inode", "boot"},
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(string, int) error
	value = func(level string, depth int) error {
		if depth > 10000 {
			return errors.New("resource inspection JSON exceeds its nesting limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			seen := make(map[string]bool)
			for decoder.More() {
				token, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok || seen[key] {
					return errors.New("resource inspection JSON has duplicate names")
				}
				seen[key] = true
				// Fold only the fixed binding keys, never arbitrary extension keys.
				for _, canonical := range known[level] {
					if key != canonical && strings.EqualFold(key, canonical) {
						return errors.New("resource inspection JSON has an aliased binding name")
					}
				}
				child := ""
				if level == "envelope" && key == "receipt" ||
					level == "receipt" && key == "owner" || level == "owner" && key == "cgroup" {
					child = key
				}
				if err := value(child, depth+1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case json.Delim('['):
			for decoder.More() {
				if err := value("", depth+1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		return nil
	}
	if err := value("envelope", 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("resource inspection JSON has trailing data")
	}
	return json.Unmarshal(data, out)
}

// InspectResourceScope recovers only the original owner bound by external exec,
// which can precede the CodexPro runner while host admission is still waiting.
// The receipt is an ownership anchor, not a current liveness or settlement proof.
func InspectResourceScope(ctx context.Context, c ResourceClaim, unit string) (*ScopeIdentity, error) {
	if err := ValidateClaim(c); err != nil {
		return nil, err
	}
	if !scopeUnitPattern.MatchString(unit) {
		return nil, errors.New("Host job resource scope was invalid.")
	}
	b, err := inspectResourceBytes(ctx, c)
	if err != nil {
		return nil, ErrHelperFailed
	}
	var inspection struct {
		Receipt *struct {
			RunID          string `json:"run_id"`
			RegistrationID string `json:"registration_id"`
			BootID         string `json:"boot_id"`
			InvocationID   string `json:"invocation_id"`
			Owner          struct {
				Kind         string `json:"kind"`
				Unit         string `json:"unit"`
				InvocationID string `json:"invocation_id"`
				Cgroup       struct {
					Path   string `json:"path"`
					Device uint64 `json:"device"`
					Inode  uint64 `json:"inode"`
					Boot   string `json:"boot"`
				} `json:"cgroup"`
			} `json:"owner"`
		} `json:"receipt"`
	}
	if err := decodeResourceInspection(b, &inspection); err != nil {
		return nil, errors.New("Host job resource response was invalid.")
	}
	if c.Version == 2 {
		if _, err := guardedReceiptBinding(b, c, unit); err != nil {
			return nil, err
		}
	}
	r := inspection.Receipt
	if r == nil || r.RunID != c.RunID || r.RegistrationID != c.RegistrationID || r.Owner.Kind != "unit" ||
		r.Owner.Unit != unit || r.InvocationID != r.Owner.InvocationID || r.BootID != r.Owner.Cgroup.Boot {
		return nil, errors.New("Host job resource owner binding was invalid.")
	}
	group := r.Owner.Cgroup
	identity := &ScopeIdentity{
		Version: 1, Unit: r.Owner.Unit, BootID: r.BootID,
		InvocationID: r.Owner.InvocationID, Cgroup: strings.TrimPrefix(group.Path, "/sys/fs/cgroup"),
	}
	if c.Version == 2 {
		identity.CgroupDevice, identity.CgroupInode = group.Device, group.Inode
	}
	if group.Device == 0 || group.Inode == 0 || !strings.HasPrefix(group.Path, "/sys/fs/cgroup/") ||
		filepath.Clean(group.Path) != group.Path || !ValidScopeIdentity(identity, unit) {
		return nil, errors.New("Host job resource owner binding was invalid.")
	}
	return identity, nil
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
	// Guarded begin may have reserved before a lost reply. Preserve its original
	// ownership for reconciliation rather than treating it as a fresh retry.
	if c.Version == 2 {
		return BeginResources(ctx, c, cwd, unit)
	}
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
	args := resourceLifecycleArgs(c, []string{"exec", c.RunID, "--registration-id", c.RegistrationID})
	return c.Helper, append(append(args, "--"), command...)
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

// CancelResources revokes a pending claim or verifies its already settled
// outcome. The helper checks the original registration before returning either.
func CancelResources(ctx context.Context, c ResourceClaim) (Settlement, error) {
	var s Settlement
	if err := invokeResource(ctx, c, []string{"cancel", c.RunID, "--registration-id", c.RegistrationID}, &s); err != nil {
		return s, err
	}
	if s.RunID == c.RunID && s.Quiescent {
		switch s.Status {
		case "cancelled":
			if s.Cancelled {
				return s, nil
			}
		case "abandoned", "failed", "succeeded":
			if !s.Cancelled {
				return s, nil
			}
		}
	}
	return s, errors.New("Host job resource settlement was not verified.")
}
