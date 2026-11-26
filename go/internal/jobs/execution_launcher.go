package jobs

import (
	"os"

	"github.com/lyeith/codexpro/go/internal/process"
)

// executionLauncherState is a read-only maintenance observation. A finished
// capture receipt permits ordinary command publication, but it does not prove
// that the independently accepted launcher process has exited.
func (s *Service) executionLauncherState(r Record, launching bool) (string, bool) {
	if r.LauncherVersion == 0 {
		return "legacy", true
	}
	if r.LauncherVersion != process.LaunchSpecVersion {
		return "unknown", false
	}
	if launching {
		return "launching", false
	}
	path := s.launcherPath(r, launcherSpecName) + ".started"
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return "unknown", false
		}
		var started process.Started
		if process.ReadJSON(path, &started, 16384) != nil {
			return "unknown", false
		}
		if started.Revoked {
			// The exclusive prevention marker has no process owner. A mixed
			// identity is inconsistent evidence, not permission to forget it.
			if started.PID != 0 || started.ProcessIdentity != "" ||
				started.CommandPID != 0 || started.CommandIdentity != "" ||
				started.ScopeIdentity != nil || started.Cgroup != "" {
				return "unknown", false
			}
			return "prevented", true
		}
		if started.PID <= 0 || started.ProcessIdentity == "" ||
			r.PID > 0 && (r.PID != started.PID || r.ProcessIdentity != "" && r.ProcessIdentity != started.ProcessIdentity) ||
			r.PID <= 0 && r.ProcessIdentity != "" {
			return "unknown", false
		}
		state := process.ObserveProcess(started.PID, started.ProcessIdentity)
		return state, state == "dead"
	}
	if !os.IsNotExist(err) {
		return "unknown", false
	}
	if r.PID > 0 && r.ProcessIdentity != "" {
		state := process.ObserveProcess(r.PID, r.ProcessIdentity)
		return state, state == "dead"
	}
	if r.PID != 0 || r.ProcessIdentity != "" {
		return "unknown", false
	}
	if r.LaunchState == "queued" || r.LaunchState == "prepared" {
		// No dispatch crossed the durable pre-spawn transition, and the
		// original launch continuation is no longer running.
		return "not_launched", true
	}
	return "unclaimed", false
}
