package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

func captureGuardedRunnerIdentity(ctx context.Context, spec Spec, specPath string, identity *ScopeIdentity) (*ScopeIdentity, error) {
	if spec.ResourceClaim == nil || spec.ResourceClaim.Version != 2 || !ValidScopeIdentity(identity, spec.ScopeUnit) {
		return nil, errors.New("guarded runner has no original finite scope")
	}
	if err := ValidateGuardedJobsState(guardedSpecJobsState(specPath)); err != nil {
		return nil, err
	}
	path := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(identity.Cgroup, "/"))
	info, err := os.Stat(path)
	if err != nil || !physicalPath(path) {
		return nil, errors.New("guarded runner physical cgroup is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev == 0 || stat.Ino == 0 || int(stat.Uid) != os.Geteuid() {
		return nil, errors.New("guarded runner cgroup identity is unavailable")
	}
	copy := *identity
	copy.CgroupDevice, copy.CgroupInode = uint64(stat.Dev), uint64(stat.Ino)
	observed, err := ObserveResourceOwnership(ctx, *spec.ResourceClaim, spec.ScopeUnit, &copy)
	if err != nil {
		return nil, err
	}
	envelope, err := guardedEnvelope(*spec.ResourceClaim, spec.TimeoutMS, spec.Nice)
	if err != nil || observed.ReceiptStatus != "running" || observed.Pending || !observed.Alive ||
		observed.Quiescent || observed.AllocationClaim != "active" || observed.GuardedEnvelope == nil ||
		*observed.GuardedEnvelope != envelope || observed.GuardedDeadlineMS != spec.Deadline {
		return nil, errors.New("guarded runner is not inside its accepted active finite owner")
	}
	return &copy, nil
}

// Linux's raw syscall returns 20 minus the user-visible nice value. Read on
// the pinned launch thread: Linux scheduling priority is a per-thread value.
func guardedThreadNice() (int, error) {
	if runtime.GOOS != "linux" {
		return 0, errors.New("guarded runner priority requires Linux")
	}
	raw, err := syscall.Getpriority(syscall.PRIO_PROCESS, 0)
	if err != nil || raw < 1 || raw > 40 {
		return 0, errors.New("guarded runner priority readback is unavailable")
	}
	return 20 - raw, nil
}

func closeGuardedBeforeDrain(ctx context.Context, spec Spec) error {
	if spec.ResourceClaim == nil {
		return nil
	}
	return CloseResources(ctx, *spec.ResourceClaim)
}
