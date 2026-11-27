package process

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const GuardedMemoryBytes uint64 = 6 * 1024 * 1024 * 1024
const GuardedTasks uint64 = 4096

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var commitPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

type ResourceControlLocator struct {
	Version      int    `json:"version"`
	ReceiptsRoot string `json:"receipts_root"`
	DefaultHome  string `json:"default_home"`
}

type FleetReleaseSnapshot struct {
	Root    string `json:"root"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	Python  string `json:"python"`
}

type ProducerSourceSnapshot struct {
	CheckoutRoot     string `json:"checkout_root"`
	ExecutablePath   string `json:"executable_path"`
	ExecutableSHA256 string `json:"executable_sha256"`
	SourceCommit     string `json:"source_commit"`
	InterpreterPath  string `json:"interpreter_path"`
}

// GuardedResourceOffer is captured before durable acceptance. Helper, Config
// and StopTimeoutUSec are connector inputs; the remaining fields are the
// unchanged producer offer. None is recaptured when a queued job is prepared.
type GuardedResourceOffer struct {
	Version          int                    `json:"version"`
	ControlLocator   ResourceControlLocator `json:"control_locator"`
	JobsState        string                 `json:"jobs_state"`
	ExecutionContext json.RawMessage        `json:"execution_context"`
	FleetRelease     FleetReleaseSnapshot   `json:"fleet_release"`
	ProducerSource   ProducerSourceSnapshot `json:"producer_source"`
	Helper           string                 `json:"helper"`
	Config           string                 `json:"config"`
	StopTimeoutUSec  uint64                 `json:"stop_timeout_usec"`
}

func CloneResourceClaim(c ResourceClaim) ResourceClaim {
	c.ExecutionContext = append(json.RawMessage(nil), c.ExecutionContext...)
	return c
}

func EqualResourceClaims(a, b ResourceClaim) bool {
	return a.Version == b.Version && a.Helper == b.Helper && a.RunID == b.RunID &&
		a.RegistrationID == b.RegistrationID && a.Config == b.Config &&
		a.ReceiptsRoot == b.ReceiptsRoot && a.DefaultHome == b.DefaultHome &&
		a.StopTimeoutUSec == b.StopTimeoutUSec && equalJSON(a.ExecutionContext, b.ExecutionContext)
}

func CloneGuardedResourceOffer(o *GuardedResourceOffer) *GuardedResourceOffer {
	if o == nil {
		return nil
	}
	copy := *o
	copy.ExecutionContext = append(json.RawMessage(nil), o.ExecutionContext...)
	return &copy
}

// A guarded claim is an exact wire contract. Legacy decoding retains the
// existing reader behavior for already accepted artifacts.
func (c *ResourceClaim) UnmarshalJSON(data []byte) error {
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return err
	}
	if header.Version == 2 {
		if _, err := exactObject(data, []string{"version", "helper", "run_id", "registration_id", "config", "receipts_root", "default_home", "execution_context", "stop_timeout_usec"}, nil); err != nil {
			return err
		}
	}
	type plain ResourceClaim
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = ResourceClaim(decoded)
	return nil
}

func equalJSON(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	canonical := func(raw []byte) ([]byte, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if _, err := decoder.Token(); err != io.EOF {
			return nil, errors.New("trailing JSON")
		}
		return json.Marshal(value)
	}
	left, err := canonical(a)
	if err != nil {
		return false
	}
	right, err := canonical(b)
	return err == nil && bytes.Equal(left, right)
}

// exactObject rejects duplicate, aliased, missing and unknown protocol keys.
// Number tokens remain unconverted, including identities larger than 2^53.
func exactObject(raw []byte, required, optional []string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := decodeResourceInspection(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("guarded resource object was invalid")
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = true
		if _, ok := fields[key]; !ok {
			return nil, errors.New("guarded resource object was incomplete")
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range fields {
		if !allowed[key] {
			return nil, errors.New("guarded resource object had an unknown field")
		}
	}
	return fields, nil
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0)
}

func physicalPath(path string) bool {
	if !cleanAbsolute(path) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && resolved == path
}

func passwdHome() (string, error) {
	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || !filepath.IsAbs(account.HomeDir) {
		return "", errors.New("guarded resources require the executing account's passwd home")
	}
	home, err := filepath.EvalSymlinks(account.HomeDir)
	if err != nil {
		return "", errors.New("guarded resources cannot resolve the account home")
	}
	return home, nil
}

func validateJobsStateAtHome(jobsState, home string) error {
	expected := filepath.Join(home, ".codexpro-go", "state")
	if jobsState != expected || !physicalPath(jobsState) {
		return errors.New("guarded resources require the physical passwd-home CodexPro state directory")
	}
	info, err := os.Stat(jobsState)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("guarded CodexPro state must be a private directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("guarded CodexPro state must belong to the executing account")
	}
	return nil
}

// ValidateGuardedJobsState never derives the supported state from HOME.
func ValidateGuardedJobsState(jobsState string) error {
	home, err := passwdHome()
	if err != nil {
		return err
	}
	return validateJobsStateAtHome(jobsState, home)
}

func validateControlLocator(locator ResourceControlLocator) error {
	root, home := locator.ReceiptsRoot, locator.DefaultHome
	parent := filepath.Dir(root)
	if locator.Version != 1 || !cleanAbsolute(root) || !cleanAbsolute(home) || filepath.Base(root) != "runs" ||
		parent == "/" || parent == "/tmp" || parent == home ||
		parent == filepath.Join(home, ".cache") || parent == filepath.Join(home, ".local", "state") {
		return errors.New("guarded resource control locator was invalid")
	}
	return nil
}

var guardedTimeoutKeys = strings.Fields(
	"heavy_exit_seconds heavy_drain_seconds heavy_adopted_child_seconds heavy_full_poll_seconds heavy_cleanup_poll_seconds " +
		"darwin_poll_seconds detached_start_seconds detached_poll_seconds command_group_settle_seconds supervisor_term_seconds " +
		"supervisor_owned_reap_seconds supervisor_post_exit_seconds supervisor_control_seconds supervisor_max_grace_seconds supervisor_poll_seconds " +
		"ssh_connect_seconds host_probe_seconds host_probe_via_seconds queue_terminate_seconds queue_settle_seconds queue_output_drain_seconds " +
		"queue_control_seconds queue_scope_query_seconds queue_scope_stop_seconds queue_scope_terminate_seconds queue_db_wait_seconds " +
		"queue_source_git_seconds queue_stack_release_seconds maintenance_cache_pass_seconds idle_stack_seconds generation_grace_seconds " +
		"landing_lock_poll_seconds landing_inventory_inner_seconds landing_inventory_kill_seconds landing_inventory_outer_seconds " +
		"stack_external_seconds stack_stop_grace_seconds stack_kill_settle_seconds stack_finite_poll_seconds stack_role_poll_seconds " +
		"stack_stop_poll_seconds stack_supervisor_start_seconds stack_supervisor_start_failure_seconds stack_supervisor_request_seconds " +
		"stack_supervisor_settle_seconds stack_supervisor_poll_seconds stack_ephemeral_probe_seconds")

type guardedLimits struct {
	MemoryBytes uint64
	Tasks       uint64
	Nice        int
}

func strictUint(raw json.RawMessage, positive bool) (uint64, error) {
	var value uint64
	if err := json.Unmarshal(raw, &value); err != nil || positive && value == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, errors.New("guarded resource integer was invalid")
	}
	return value, nil
}

func validateExecutionContext(raw json.RawMessage) (guardedLimits, error) {
	var zero guardedLimits
	fields, err := exactObject(raw, []string{"schema_version", "config_revision", "config_sha256", "resources", "timeouts", "priorities"}, []string{"worker_defaults"})
	if err != nil {
		return zero, err
	}
	version, err := strictUint(fields["schema_version"], true)
	var revision, digest string
	if err != nil || version != 1 || json.Unmarshal(fields["config_revision"], &revision) != nil ||
		strings.TrimSpace(revision) == "" || json.Unmarshal(fields["config_sha256"], &digest) != nil || !digestPattern.MatchString(digest) {
		return zero, errors.New("guarded execution context identity was invalid")
	}
	resources, err := exactObject(fields["resources"], []string{"stack_memory_mib", "stack_tasks"},
		[]string{"go_parallelism", "go_memory_mib", "node_heap_mib", "frontend_workers"})
	if err != nil {
		return zero, err
	}
	values := make(map[string]uint64, len(resources))
	for key, raw := range resources {
		value, err := strictUint(raw, true)
		if err != nil {
			return zero, err
		}
		values[key] = value
	}
	if values["stack_memory_mib"] > math.MaxUint64/(1024*1024) {
		return zero, errors.New("guarded memory limit overflowed")
	}
	priorities, err := exactObject(fields["priorities"], []string{"stream_nice", "ionice_class", "ionice_priority"}, nil)
	if err != nil {
		return zero, err
	}
	nice, err := strictUint(priorities["stream_nice"], false)
	class, classErr := strictUint(priorities["ionice_class"], true)
	priority, priorityErr := strictUint(priorities["ionice_priority"], false)
	if err != nil || classErr != nil || priorityErr != nil || nice > 19 || class > 3 || priority > 7 {
		return zero, errors.New("guarded execution priorities were invalid")
	}
	timeouts, err := exactObject(fields["timeouts"], guardedTimeoutKeys, nil)
	if err != nil {
		return zero, err
	}
	for _, raw := range timeouts {
		text := strings.TrimSpace(string(raw))
		if text == "" || text[0] < '0' || text[0] > '9' {
			return zero, errors.New("guarded execution timeout was not a positive number")
		}
		var number json.Number
		if err := json.Unmarshal(raw, &number); err != nil {
			return zero, errors.New("guarded execution timeout was invalid")
		}
		value, err := strconv.ParseFloat(string(number), 64)
		if err != nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return zero, errors.New("guarded execution timeout was invalid")
		}
	}
	if raw, ok := fields["worker_defaults"]; ok {
		defaults, err := exactObject(raw, []string{"go_parallelism", "frontend_workers", "gradle_workers"}, nil)
		if err != nil {
			return zero, err
		}
		for _, raw := range defaults {
			if _, err := strictUint(raw, true); err != nil {
				return zero, err
			}
		}
	}
	return guardedLimits{MemoryBytes: values["stack_memory_mib"] * 1024 * 1024, Tasks: values["stack_tasks"], Nice: int(nice)}, nil
}

func (o GuardedResourceOffer) validateSnapshot(jobsState string) error {
	if o.Version != 1 || o.JobsState != jobsState || !cleanAbsolute(jobsState) ||
		!cleanAbsolute(o.Helper) || !cleanAbsolute(o.Config) || o.StopTimeoutUSec == 0 || o.StopTimeoutUSec == math.MaxUint64 {
		return errors.New("guarded resource offer binding was invalid")
	}
	if err := validateControlLocator(o.ControlLocator); err != nil {
		return err
	}
	limits, err := validateExecutionContext(o.ExecutionContext)
	if err != nil {
		return err
	}
	if limits.MemoryBytes != GuardedMemoryBytes || limits.Tasks != GuardedTasks {
		return errors.New("the selected guarded route requires the accepted 6 GiB and 4096-task envelope")
	}
	source, release := o.ProducerSource, o.FleetRelease
	if !cleanAbsolute(source.CheckoutRoot) || source.ExecutablePath != o.Helper ||
		source.ExecutablePath != filepath.Join(source.CheckoutRoot, "bin", "ssd-dev") ||
		!digestPattern.MatchString(source.ExecutableSHA256) || !commitPattern.MatchString(source.SourceCommit) ||
		!cleanAbsolute(source.InterpreterPath) || !cleanAbsolute(release.Root) ||
		!cleanAbsolute(release.Python) || strings.TrimSpace(release.Version) == "" ||
		!digestPattern.MatchString(release.Digest) || filepath.Base(release.Root) != release.Digest {
		return errors.New("guarded resource offer source pins were invalid")
	}
	return nil
}

// Validate checks a retained offer against the actual resolved instance state.
// The passwd-home restriction was checked by CaptureGuardedResources before
// acceptance; delayed preparation must not capture a different policy or home.
func (o GuardedResourceOffer) Validate(jobsState string) error {
	return o.validateSnapshot(jobsState)
}

func (o GuardedResourceOffer) Claim(run, registration string) (ResourceClaim, error) {
	if err := o.validateSnapshot(o.JobsState); err != nil {
		return ResourceClaim{}, err
	}
	claim := ResourceClaim{Version: 2, Helper: o.Helper, RunID: run, RegistrationID: registration,
		Config: o.Config, ReceiptsRoot: o.ControlLocator.ReceiptsRoot, DefaultHome: o.ControlLocator.DefaultHome,
		ExecutionContext: append(json.RawMessage(nil), o.ExecutionContext...), StopTimeoutUSec: o.StopTimeoutUSec}
	return claim, ValidateClaim(claim)
}

func decodeGuardedOffer(data []byte, helper, config, jobsState string, stopUSec uint64) (*GuardedResourceOffer, error) {
	fields, err := exactObject(data, []string{"version", "control_locator", "jobs_state", "execution_context", "fleet_release", "producer_source"}, nil)
	if err != nil {
		return nil, err
	}
	for key, required := range map[string][]string{
		"control_locator": {"version", "receipts_root", "default_home"},
		"fleet_release":   {"root", "version", "digest", "python"},
		"producer_source": {"checkout_root", "executable_path", "executable_sha256", "source_commit", "interpreter_path"},
	} {
		if _, err := exactObject(fields[key], required, nil); err != nil {
			return nil, err
		}
	}
	var offer GuardedResourceOffer
	if err := json.Unmarshal(data, &offer); err != nil {
		return nil, errors.New("guarded resource offer was invalid")
	}
	offer.Helper, offer.Config, offer.StopTimeoutUSec = helper, config, stopUSec
	if err := offer.validateSnapshot(jobsState); err != nil {
		return nil, err
	}
	return &offer, nil
}

// CaptureGuardedResources is a read-only preacceptance offer. It does not
// reserve an owner or return a launch grant.
func CaptureGuardedResources(ctx context.Context, helper, config, jobsState string, stopUSec uint64) (*GuardedResourceOffer, error) {
	if err := ValidateGuardedJobsState(jobsState); err != nil {
		return nil, err
	}
	if !physicalPath(helper) || !physicalPath(config) || stopUSec == 0 || stopUSec == math.MaxUint64 {
		return nil, errors.New("guarded resources require physical absolute helper and configuration paths")
	}
	data, err := RunBounded(ctx, 10*time.Second, 1024*1024, helper, "--config", config, "external", "capture-context", "--jobs-state", jobsState)
	if err != nil {
		return nil, ErrHelperFailed
	}
	offer, err := decodeGuardedOffer(data, helper, config, jobsState, stopUSec)
	if err != nil {
		return nil, err
	}
	home, err := passwdHome()
	if err != nil || offer.FleetRelease.Root != filepath.Join(home, ".local", "share", "fleet-tools", "releases", offer.FleetRelease.Digest) {
		return nil, errors.New("guarded Fleet release does not belong to the executing account")
	}
	for _, path := range []string{offer.FleetRelease.Root, offer.FleetRelease.Python, offer.ProducerSource.CheckoutRoot, offer.ProducerSource.InterpreterPath} {
		if !physicalPath(path) {
			return nil, errors.New("guarded source pins do not name physical paths")
		}
	}
	file, err := os.Open(helper)
	if err != nil {
		return nil, errors.New("guarded helper is unavailable")
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, 1024*1024+1)); err != nil {
		return nil, errors.New("guarded helper could not be verified")
	}
	if hex.EncodeToString(hash.Sum(nil)) != offer.ProducerSource.ExecutableSHA256 {
		return nil, errors.New("guarded helper source pin changed")
	}
	return offer, nil
}
