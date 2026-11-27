package process

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var scopeUnitPattern = regexp.MustCompile(`^codexpro-job_[a-f0-9]+-[a-f0-9]{32}\.scope$`)
var bootPattern = regexp.MustCompile(`^[a-f0-9-]{36}$`)

type ScopeState string

const (
	ScopeActive    ScopeState = "active"
	ScopeQuiescent ScopeState = "quiescent"
	ScopeUnknown   ScopeState = "unknown"
	ScopeMismatch  ScopeState = "mismatch"
)

func itoa(n int) string { return strconv.Itoa(n) }

func BootID() (string, error) {
	b, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b)), e
}
func SelfCgroup() (string, error) {
	b, e := os.ReadFile("/proc/self/cgroup")
	if e != nil {
		return "", e
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "0::/") {
			return strings.TrimPrefix(line, "0::"), nil
		}
	}
	return "", errors.New("unified cgroup membership is unavailable")
}

func ValidScopeIdentity(s *ScopeIdentity, unit string) bool {
	return s != nil && s.Version == 1 && scopeUnitPattern.MatchString(s.Unit) && (unit == "" || s.Unit == unit) && bootPattern.MatchString(s.BootID) && claimIDPattern.MatchString(s.InvocationID) && strings.HasPrefix(s.Cgroup, "/") && filepath.Base(s.Cgroup) == s.Unit && !strings.Contains("/"+s.Cgroup+"/", "/../")
}

func scopeProperties(ctx context.Context, unit string) (map[string]string, error) {
	b, e := RunBounded(ctx, 2*time.Second, 64*1024, "systemctl", "--user", "show", "--property=LoadState,ActiveState,InvocationID,ControlGroup", unit)
	if e != nil {
		return nil, e
	}
	m := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			m[k] = v
		}
	}
	return m, nil
}

func CaptureScopeIdentity(ctx context.Context, unit string) (*ScopeIdentity, error) {
	p, e := scopeProperties(ctx, unit)
	if e != nil {
		return nil, e
	}
	boot, e := BootID()
	if e != nil {
		return nil, e
	}
	self, e := SelfCgroup()
	if e != nil {
		return nil, e
	}
	s := &ScopeIdentity{Version: 1, Unit: unit, BootID: boot, InvocationID: p["InvocationID"], Cgroup: p["ControlGroup"]}
	if !ValidScopeIdentity(s, unit) || p["LoadState"] != "loaded" || p["ActiveState"] != "active" || self != s.Cgroup {
		return nil, errors.New("The job supervisor is not inside its registered systemd scope.")
	}
	return s, nil
}

func InspectScope(ctx context.Context, s *ScopeIdentity) ScopeState {
	if !ValidScopeIdentity(s, "") {
		return ScopeUnknown
	}
	guarded := s.CgroupDevice != 0 || s.CgroupInode != 0
	if guarded && (s.CgroupDevice == 0 || s.CgroupInode == 0) {
		return ScopeUnknown
	}
	boot, e := BootID()
	if e != nil {
		return ScopeUnknown
	}
	if boot != s.BootID {
		if guarded {
			return ScopeUnknown
		}
		return ScopeQuiescent
	}
	p, e := scopeProperties(ctx, s.Unit)
	if e != nil {
		return ScopeUnknown
	}
	absent := p["LoadState"] == "not-found"
	if !absent && (p["InvocationID"] != s.InvocationID || p["ControlGroup"] != s.Cgroup) {
		return ScopeMismatch
	}
	root := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(s.Cgroup, "/"))
	if guarded {
		info, err := os.Stat(root)
		if err != nil || !physicalPath(root) {
			// Only the original common owner can prove physical removal.
			return ScopeUnknown
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return ScopeUnknown
		}
		if uint64(stat.Dev) != s.CgroupDevice || uint64(stat.Ino) != s.CgroupInode {
			return ScopeMismatch
		}
	}
	active := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(filepath.Join(path, "cgroup.procs"))
		if e != nil {
			if os.IsNotExist(e) {
				return nil
			}
			return e
		}
		if strings.TrimSpace(string(b)) != "" {
			active = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return ScopeUnknown
	}
	if !active {
		if guarded && !absent && (p["ActiveState"] == "active" || p["ActiveState"] == "activating" || p["ActiveState"] == "reloading") {
			return ScopeActive
		}
		return ScopeQuiescent
	}
	if absent {
		return ScopeUnknown
	}
	return ScopeActive
}

func SignalScope(ctx context.Context, s *ScopeIdentity, signal syscall.Signal) bool {
	if InspectScope(ctx, s) != ScopeActive {
		return false
	}
	_, e := RunBounded(ctx, 2*time.Second, 4096, "systemctl", "--user", "kill", "--kill-whom=all", "--signal="+signalName(signal), s.Unit)
	return e == nil
}

func SettleScope(ctx context.Context, s *ScopeIdentity) bool {
	switch InspectScope(ctx, s) {
	case ScopeQuiescent:
		return true
	case ScopeActive:
	default:
		return false
	}
	if !SignalScope(ctx, s, syscall.SIGKILL) {
		return false
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
		switch InspectScope(ctx, s) {
		case ScopeQuiescent:
			return true
		case ScopeActive:
		default:
			return false
		}
	}
	return false
}

func ProbeScopes(ctx context.Context) error {
	_, err := ProbeScopesWithUnit(ctx)
	return err
}

// ProbeScopesWithUnit preserves the fixed capability probe and returns the
// exact systemd scope name even when its command fails. An error is not proof
// that the attempted scope is quiescent.
func ProbeScopesWithUnit(ctx context.Context) (string, error) {
	suffix, err := RandomHex(4)
	if err != nil {
		return "", err
	}
	unit := "codexpro-go-probe-" + itoa(os.Getpid()) + "-" + suffix
	_, err = RunBounded(ctx, 5*time.Second, 4096, "systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit", unit, "--", "true")
	return unit + ".scope", err
}

func procFields(pid int) ([]string, error) {
	b, e := os.ReadFile(filepath.Join("/proc", itoa(pid), "stat"))
	if e != nil {
		return nil, e
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return nil, errors.New("invalid process stat")
	}
	f := strings.Fields(string(b)[i+1:])
	if len(f) < 20 {
		return nil, errors.New("short process stat")
	}
	return f, nil
}
func ProcessIdentity(pid int) string {
	f, e := procFields(pid)
	if e != nil {
		return ""
	}
	return f[19]
}
func ObserveProcess(pid int, identity string) string {
	if pid <= 0 {
		return "dead"
	}
	f, e := procFields(pid)
	if e != nil {
		if os.IsNotExist(e) {
			return "dead"
		}
		return "unknown"
	}
	if f[0] == "Z" || f[0] == "X" {
		return "dead"
	}
	if identity == "" {
		return "unknown"
	}
	if f[19] != identity {
		return "dead"
	}
	return "active"
}
func ProcessAlive(pid int, identity string) bool { return ObserveProcess(pid, identity) == "active" }

// Process groups are a local runner cleanup aid, never a replacement for a scope proof.
func GroupAlive(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	entries, e := os.ReadDir("/proc")
	if e != nil {
		return true
	}
	wanted := itoa(pgid)
	for _, entry := range entries {
		pid, e := strconv.Atoi(entry.Name())
		if e != nil {
			continue
		}
		f, e := procFields(pid)
		if e != nil {
			continue
		}
		if f[2] == wanted && f[0] != "Z" && f[0] != "X" {
			return true
		}
	}
	return false
}

func SignalGroup(pgid int, signal syscall.Signal) error {
	if pgid <= 0 {
		return errors.New("invalid process group")
	}
	e := syscall.Kill(-pgid, signal)
	if e == syscall.ESRCH {
		return nil
	}
	return e
}

func DrainGroup(ctx context.Context, pgid int) bool {
	return drainGroupUntil(ctx, pgid, time.Time{})
}

// A nonzero deadline is the original monotonic TERM deadline. Main-process
// exit must not renew or shorten the remaining allowance for its descendants.
func drainGroupUntil(ctx context.Context, pgid int, until time.Time) bool {
	if !GroupAlive(pgid) {
		return true
	}
	_ = SignalGroup(pgid, syscall.SIGTERM)
	if until.IsZero() {
		until = time.Now().Add(legacyStopEscalation)
	}
	for time.Now().Before(until) {
		if !GroupAlive(pgid) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	_ = SignalGroup(pgid, syscall.SIGKILL)
	until = time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		if !GroupAlive(pgid) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	return !GroupAlive(pgid)
}

func signalName(s syscall.Signal) string {
	switch s {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGABRT:
		return "SIGABRT"
	default:
		return "SIG" + itoa(int(s))
	}
}
