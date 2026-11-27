package process

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The margin is subtracted before activation, never added to the accepted
// deadline. SSD verifies ActiveEnterTimestamp + actual RuntimeMaxUSec before
// allowing the payload. A delayed startup must fail closed, not renew time.
const GuardedStartupAllowance = 2 * time.Second

type GuardedUnitEnvelope struct {
	MemoryBytes uint64 `json:"memory_bytes"`
	SwapBytes   uint64 `json:"swap_bytes"`
	Tasks       uint64 `json:"tasks"`
	RuntimeUSec uint64 `json:"runtime_usec"`
	StopUSec    uint64 `json:"stop_usec"`
	Nice        int    `json:"nice"`
}

type ResourceMemoryKey struct {
	HostID         string `json:"host_id"`
	BootID         string `json:"boot_id"`
	Producer       string `json:"producer"`
	RunID          string `json:"run_id"`
	RegistrationID string `json:"registration_id"`
}

type ResourceActivation struct {
	Schema         int               `json:"schema"`
	Key            ResourceMemoryKey `json:"key"`
	BindingDigest  string            `json:"binding_digest"`
	State          string            `json:"state"`
	TransitionMade bool              `json:"transition_made"`
	ChargedBytes   uint64            `json:"charged_bytes"`
	ParentKey      json.RawMessage   `json:"parent_key"`
	KernelOwner    json.RawMessage   `json:"kernel_owner"`
}

type guardedMemoryBinding struct {
	Key                 ResourceMemoryKey      `json:"key"`
	FleetRelease        FleetReleaseSnapshot   `json:"fleet_release"`
	ProducerSource      ProducerSourceSnapshot `json:"producer_source"`
	ExecutionContext    json.RawMessage        `json:"execution_context"`
	UnitEnvelope        GuardedUnitEnvelope    `json:"unit_envelope"`
	DeadlineMS          int64                  `json:"deadline_ms"`
	OriginatingUpstream json.RawMessage        `json:"originating_upstream"`
	BindingDigest       string                 `json:"binding_digest"`
}

func validateGuardedClaim(c ResourceClaim) error {
	if !cleanAbsolute(c.Helper) || !cleanAbsolute(c.Config) || c.StopTimeoutUSec == 0 || c.StopTimeoutUSec == math.MaxUint64 {
		return errors.New("guarded resource claim control binding was invalid")
	}
	if err := validateControlLocator(ResourceControlLocator{Version: 1, ReceiptsRoot: c.ReceiptsRoot, DefaultHome: c.DefaultHome}); err != nil {
		return err
	}
	limits, err := validateExecutionContext(c.ExecutionContext)
	if err != nil {
		return err
	}
	if limits.MemoryBytes != GuardedMemoryBytes || limits.Tasks != GuardedTasks {
		return errors.New("guarded resource claim differs from the selected 6 GiB envelope")
	}
	return nil
}

func guardedEnvelope(c ResourceClaim, timeoutMS int64, nice int) (GuardedUnitEnvelope, error) {
	var zero GuardedUnitEnvelope
	if c.Version != 2 || timeoutMS <= 0 || uint64(timeoutMS) >= math.MaxUint64/1000 {
		return zero, errors.New("guarded resource lifetime was invalid")
	}
	if err := ValidateClaim(c); err != nil {
		return zero, err
	}
	limits, err := validateExecutionContext(c.ExecutionContext)
	if err != nil {
		return zero, err
	}
	if nice != limits.Nice {
		return zero, errors.New("guarded runner priority differs from its accepted execution context")
	}
	return GuardedUnitEnvelope{MemoryBytes: limits.MemoryBytes, Tasks: limits.Tasks,
		RuntimeUSec: uint64(timeoutMS) * 1000, StopUSec: c.StopTimeoutUSec, Nice: nice}, nil
}

func resourceLifecycleArgs(c ResourceClaim, args []string) []string {
	out := []string{"external"}
	if c.Version == 2 {
		out = []string{"--config", c.Config, "external"}
	}
	out = append(out, args...)
	if c.Version == 2 {
		out = append(out, "--receipts-root", c.ReceiptsRoot, "--default-home", c.DefaultHome)
	}
	return out
}

func resourceInspectionArgs(c ResourceClaim) []string {
	if c.Version == 2 {
		return []string{"external", "inspect", c.RunID, "--registration-id", c.RegistrationID,
			"--receipts-root", c.ReceiptsRoot, "--default-home", c.DefaultHome}
	}
	return []string{"inspect", c.RunID}
}

func inspectResourceBytes(ctx context.Context, c ResourceClaim) ([]byte, error) {
	if err := ValidateClaim(c); err != nil {
		return nil, err
	}
	data, err := RunBounded(ctx, 10*time.Second, 1024*1024, c.Helper, resourceInspectionArgs(c)...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, ErrHelperFailed
	}
	return data, nil
}

func decodeMemoryBinding(raw json.RawMessage, c ResourceClaim) (guardedMemoryBinding, error) {
	var memory guardedMemoryBinding
	fields, err := exactObject(raw, []string{"key", "fleet_release", "producer_source", "execution_context", "unit_envelope",
		"deadline_ms", "originating_upstream", "binding_digest"}, nil)
	if err != nil {
		return memory, err
	}
	for key, required := range map[string][]string{
		"key":                  {"host_id", "boot_id", "producer", "run_id", "registration_id"},
		"fleet_release":        {"root", "version", "digest", "python"},
		"producer_source":      {"checkout_root", "executable_path", "executable_sha256", "source_commit", "interpreter_path"},
		"unit_envelope":        {"memory_bytes", "swap_bytes", "tasks", "runtime_usec", "stop_usec", "nice"},
		"originating_upstream": {"producer", "job_id", "binding_sha256"},
	} {
		if _, err := exactObject(fields[key], required, nil); err != nil {
			return memory, err
		}
	}
	if err := json.Unmarshal(raw, &memory); err != nil {
		return memory, errors.New("guarded resource receipt memory binding was invalid")
	}
	key := memory.Key
	if !claimIDPattern.MatchString(key.HostID) || !observationBootPattern.MatchString(key.BootID) ||
		key.Producer != "ssd-dev" || key.RunID != c.RunID || key.RegistrationID != c.RegistrationID ||
		!digestPattern.MatchString(memory.BindingDigest) || !equalJSON(memory.ExecutionContext, c.ExecutionContext) ||
		memory.ProducerSource.ExecutablePath != c.Helper || memory.DeadlineMS <= 0 ||
		memory.UnitEnvelope.RuntimeUSec == 0 || memory.UnitEnvelope.RuntimeUSec == math.MaxUint64 ||
		memory.UnitEnvelope.StopUSec != c.StopTimeoutUSec || memory.UnitEnvelope.SwapBytes != 0 {
		return memory, errors.New("guarded resource receipt changed its accepted binding")
	}
	limits, err := validateExecutionContext(memory.ExecutionContext)
	if err != nil || memory.UnitEnvelope.MemoryBytes != limits.MemoryBytes || memory.UnitEnvelope.Tasks != limits.Tasks || memory.UnitEnvelope.Nice != limits.Nice {
		return memory, errors.New("guarded resource receipt changed its finite envelope")
	}
	return memory, nil
}

func guardedReceiptBinding(data []byte, c ResourceClaim, unit string) (guardedMemoryBinding, error) {
	var zero guardedMemoryBinding
	var inspection struct {
		Receipt *struct {
			RunID          string          `json:"run_id"`
			RegistrationID string          `json:"registration_id"`
			BootID         string          `json:"boot_id"`
			HostMemory     json.RawMessage `json:"host_memory"`
			Owner          *struct {
				Kind string `json:"kind"`
				Unit string `json:"unit"`
			} `json:"owner"`
		} `json:"receipt"`
	}
	if err := decodeResourceInspection(data, &inspection); err != nil {
		return zero, errors.New("guarded resource inspection was invalid")
	}
	r := inspection.Receipt
	if r == nil || r.RunID != c.RunID || r.RegistrationID != c.RegistrationID || r.Owner == nil ||
		r.Owner.Kind != "unit" || !scopeUnitPattern.MatchString(r.Owner.Unit) || unit != "" && r.Owner.Unit != unit {
		return zero, errors.New("guarded resource original receipt was not established")
	}
	memory, err := decodeMemoryBinding(r.HostMemory, c)
	if err != nil || memory.Key.BootID != r.BootID {
		return zero, errors.New("guarded resource original memory owner was not established")
	}
	return memory, nil
}

func decodeActivation(data []byte, c ResourceClaim, original guardedMemoryBinding) (ResourceActivation, error) {
	var result ResourceActivation
	fields, err := exactObject(data, []string{"schema", "key", "binding_digest", "state", "transition_made", "charged_bytes", "parent_key", "kernel_owner"}, nil)
	if err != nil {
		return result, err
	}
	if _, err := exactObject(fields["key"], []string{"host_id", "boot_id", "producer", "run_id", "registration_id"}, nil); err != nil {
		return result, err
	}
	var made *bool
	if json.Unmarshal(fields["transition_made"], &made) != nil || made == nil {
		return result, errors.New("guarded resource transition result was incomplete")
	}
	if err := json.Unmarshal(data, &result); err != nil || result.Schema != 1 || result.Key != original.Key ||
		result.Key.RunID != c.RunID || result.Key.RegistrationID != c.RegistrationID || result.BindingDigest != original.BindingDigest {
		return result, errors.New("guarded resource transition changed the original owner")
	}
	return result, nil
}

func ActivateResources(ctx context.Context, c ResourceClaim, spec Spec, offer *GuardedResourceOffer) (ResourceActivation, error) {
	var zero ResourceActivation
	if c.Version != 2 || offer == nil || spec.Version != GuardedRunnerSpecVersion || spec.ResourceClaim == nil ||
		!EqualResourceClaims(c, *spec.ResourceClaim) || !scopeUnitPattern.MatchString(spec.ScopeUnit) {
		return zero, errors.New("guarded activation has no matching original specification")
	}
	if err := offer.validateSnapshot(offer.JobsState); err != nil {
		return zero, err
	}
	expected, err := offer.Claim(c.RunID, c.RegistrationID)
	if err != nil || !EqualResourceClaims(expected, c) {
		return zero, errors.New("guarded activation changed its original offer")
	}
	envelope, err := guardedEnvelope(c, spec.TimeoutMS, spec.Nice)
	if err != nil || spec.Deadline <= time.Now().UnixMilli() {
		return zero, errors.New("guarded activation has no remaining original lifetime")
	}
	data, err := inspectResourceBytes(ctx, c)
	if err != nil {
		return zero, err
	}
	if err := validateGuardedPreparedInspection(data); err != nil {
		return zero, err
	}
	original, err := guardedReceiptBinding(data, c, spec.ScopeUnit)
	if err != nil {
		return zero, err
	}
	if original.UnitEnvelope != envelope || original.DeadlineMS != spec.Deadline ||
		original.FleetRelease != offer.FleetRelease || original.ProducerSource != offer.ProducerSource {
		return zero, errors.New("guarded activation differs from the accepted offer and deadline")
	}
	data, err = RunBounded(ctx, 10*time.Second, 16*1024, c.Helper,
		resourceLifecycleArgs(c, []string{"activate", c.RunID, "--registration-id", c.RegistrationID})...)
	if err != nil {
		// The transition may have happened. This error grants neither a second
		// launch nor revocation of the original charged claim.
		return zero, errors.New("guarded activation is unresolved; retain the original owner")
	}
	result, err := decodeActivation(data, c, original)
	if err != nil {
		return zero, err
	}
	if result.State != "launching" || !result.TransitionMade || result.ChargedBytes != envelope.MemoryBytes ||
		strings.TrimSpace(string(result.ParentKey)) != "null" || strings.TrimSpace(string(result.KernelOwner)) != "null" {
		return zero, errors.New("guarded activation did not grant one fresh owner launch")
	}
	return result, nil
}

// CloseResources fences the original guarded owner before any intentional
// drain. Legacy owners retain their original finish/cancel protocol.
func CloseResources(ctx context.Context, c ResourceClaim) error {
	if c.Version == 1 {
		return nil
	}
	data, err := inspectResourceBytes(ctx, c)
	if err != nil {
		return err
	}
	var inspection resourceInspection
	if err := decodeResourceInspection(data, &inspection); err != nil {
		return err
	}
	if inspection.Receipt != nil && inspection.Receipt.Owner != nil && inspection.Receipt.Owner.Kind == "revoked" {
		_, err := observeGuardedTombstone(data, c, inspection, nil)
		return err
	}
	original, err := guardedReceiptBinding(data, c, "")
	if err != nil {
		return err
	}
	data, err = RunBounded(ctx, 10*time.Second, 1024*1024, c.Helper,
		resourceLifecycleArgs(c, []string{"close", c.RunID, "--registration-id", c.RegistrationID})...)
	if err != nil {
		return ErrHelperFailed
	}
	result, err := decodeActivation(data, c, original)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(result.ParentKey)) != "null" {
		return errors.New("guarded independent owner unexpectedly borrowed a parent")
	}
	switch result.State {
	case "closing":
		if result.ChargedBytes == original.UnitEnvelope.MemoryBytes {
			return nil
		}
	case "released", "revoked":
		if result.ChargedBytes == 0 {
			return nil
		}
	}
	return errors.New("guarded resource close did not fence its original owner")
}

func GuardedScopeCommand(c ResourceClaim, spec Spec, command []string, now time.Time) (string, []string, error) {
	if spec.Version != GuardedRunnerSpecVersion || spec.ResourceClaim == nil || !EqualResourceClaims(c, *spec.ResourceClaim) ||
		!scopeUnitPattern.MatchString(spec.ScopeUnit) || len(command) == 0 || command[0] == "" || spec.Deadline <= 0 ||
		spec.Deadline > math.MaxInt64/1000 || now.UnixMicro() < 0 {
		return "", nil, errors.New("guarded scope command has no valid original owner")
	}
	envelope, err := guardedEnvelope(c, spec.TimeoutMS, spec.Nice)
	if err != nil {
		return "", nil, err
	}
	remaining := spec.Deadline*1000 - now.UnixMicro() - GuardedStartupAllowance.Microseconds()
	if remaining <= 0 {
		return "", nil, errors.New("guarded scope has insufficient original lifetime for bounded startup")
	}
	runtime := min(envelope.RuntimeUSec, uint64(remaining))
	properties := []string{
		"MemoryMax=" + strconv.FormatUint(envelope.MemoryBytes, 10), "MemorySwapMax=0",
		"TasksMax=" + strconv.FormatUint(envelope.Tasks, 10),
		"RuntimeMaxSec=" + strconv.FormatUint(runtime, 10) + "us",
		"RuntimeRandomizedExtraSec=0", "TimeoutStopSec=" + strconv.FormatUint(envelope.StopUSec, 10) + "us",
		"KillMode=control-group", "SendSIGKILL=yes",
	}
	args := []string{"--user", "--scope", "--quiet", "--collect", "--unit", strings.TrimSuffix(spec.ScopeUnit, ".scope")}
	for _, property := range properties {
		args = append(args, "--property="+property)
	}
	return "systemd-run", append(append(args, "--"), command...), nil
}

// The exact physical state is checked during capture; delayed preparation
// reuses the saved offer and resolved instance path without recapturing.
func (o GuardedResourceOffer) ExecutionNice() (int, error) {
	limits, err := validateExecutionContext(o.ExecutionContext)
	return limits.Nice, err
}

func guardedSpecJobsState(specPath string) string {
	return filepath.Dir(filepath.Dir(specPath))
}
