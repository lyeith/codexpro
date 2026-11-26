package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
)

const (
	launcherSpecName   = "launcher.spec.json"
	launcherResultName = "launcher.result.json"
	launcherStdoutName = "launcher-stdout.log"
	launcherStderrName = "launcher-stderr.log"
)

func (s *Service) launcherPath(r Record, name string) string {
	return filepath.Join(s.outputDir(r), name)
}

// The selection belongs to the accepted record. Legacy queued and prepared
// jobs keep their original launcher instead of acquiring a new process on upgrade.
func (s *Service) launcherCommand(r Record, o startOptions, name string, args []string) (string, []string, error) {
	if r.LauncherVersion == 0 {
		return name, args, nil
	}
	if r.LauncherVersion != process.LaunchSpecVersion {
		return "", nil, fmt.Errorf("unsupported accepted launcher version %d", r.LauncherVersion)
	}
	spec := process.LaunchSpec{
		Version:        process.LaunchSpecVersion,
		Args:           append([]string{name}, args...),
		Cwd:            o.CwdAbs,
		Stdout:         s.launcherPath(r, launcherStdoutName),
		Stderr:         s.launcherPath(r, launcherStderrName),
		Result:         s.launcherPath(r, launcherResultName),
		Limit:          min(r.OutputLimitBytes, process.LaunchDiagnosticLimit),
		PathRedactions: s.paths(o.Workspace),
	}
	path := s.launcherPath(r, launcherSpecName)
	if err := process.WriteJSON(path, spec); err != nil {
		return "", nil, err
	}
	return s.launcherExecutable, []string{"launcher", path}, nil
}

func copyLaunchResult(result *process.LaunchResult) *process.LaunchResult {
	if result == nil {
		return nil
	}
	out := *result
	if result.ExitCode != nil {
		value := *result.ExitCode
		out.ExitCode = &value
	}
	if result.Signal != nil {
		value := *result.Signal
		out.Signal = &value
	}
	return &out
}

// A launch error remains in the existing durable job even if its output
// directory cannot be written. It is not an observed payload exit or signal.
func (s *Service) rememberLaunchError(r Record, o startOptions, cause error) Record {
	if r.LauncherVersion == 0 {
		return r
	}
	result := process.LaunchResult{
		Version:      process.LaunchResultVersion,
		Error:        process.LaunchErrorText(cause, s.paths(o.Workspace)),
		FinishedAtMS: time.Now().UnixMilli(),
	}
	// An independent launcher may already have published the actual outer
	// command result. Never replace that observation with a supervisor error.
	var observed process.LaunchResult
	if process.ReadJSON(s.launcherPath(r, launcherResultName), &observed, 32768) == nil && validLaunchResult(observed) {
		result = observed
	}
	// Supervisor fallbacks stay in the durable job record. Writing a new file
	// here could race retention after the independent process has exited.
	next, err := s.update(r.ID, func(next *Record) bool {
		if next.LaunchResult != nil {
			return false
		}
		next.LaunchResult = copyLaunchResult(&result)
		return true
	})
	if err == nil {
		return next
	}
	r.LaunchResult = copyLaunchResult(&result)
	return r
}

func validLaunchResult(result process.LaunchResult) bool {
	if result.Version != process.LaunchResultVersion || result.FinishedAtMS <= 0 || int64(len(result.Error)) > process.LaunchDiagnosticLimit {
		return false
	}
	if result.ExitCode != nil && (*result.ExitCode < 0 || *result.ExitCode > 255) {
		return false
	}
	return result.Signal == nil || len(*result.Signal) <= 64
}

func (s *Service) observeLauncher(r Record) Record {
	if r.LauncherVersion != process.LaunchSpecVersion || r.LaunchResult != nil {
		return r
	}
	var result process.LaunchResult
	if process.ReadJSON(s.launcherPath(r, launcherResultName), &result, 32768) != nil || !validLaunchResult(result) {
		return r
	}
	next, err := s.update(r.ID, func(next *Record) bool {
		if next.LaunchResult != nil {
			return false
		}
		next.LaunchResult = copyLaunchResult(&result)
		return true
	})
	if err == nil {
		return next
	}
	return r
}

// The durable marker recovers the capture process when the service died before
// saving its PID. A conflicting or unreadable identity is not evidence of death.
func (s *Service) launcherState(r Record) string {
	var started process.Started
	err := process.ReadJSON(s.launcherPath(r, launcherSpecName)+".started", &started, 16384)
	if err == nil {
		if started.Revoked {
			return "dead"
		}
		if started.PID <= 0 || started.ProcessIdentity == "" ||
			r.PID > 0 && (r.PID != started.PID || r.ProcessIdentity != "" && r.ProcessIdentity != started.ProcessIdentity) {
			return "unknown"
		}
		return process.ObserveProcess(started.PID, started.ProcessIdentity)
	}
	if !os.IsNotExist(err) {
		return "unknown"
	}
	if r.PID > 0 {
		return process.ObserveProcess(r.PID, r.ProcessIdentity)
	}
	return "unclaimed"
}

// Terminal publication also permits retention. Keep it behind the independent
// writer's receipt or proven death, and revoke an unclaimed writer before cleanup
// so a delayed process cannot begin capturing after its job has been removed.
func (s *Service) launcherCaptureReady(r Record) (Record, bool) {
	if r.LauncherVersion == 0 {
		return r, true
	}
	r = s.observeLauncher(r)
	if r.LaunchResult != nil && validLaunchResult(*r.LaunchResult) {
		return r, true
	}
	state := s.launcherState(r)
	if state != "dead" && state != "unclaimed" {
		return r, false
	}
	var started process.Started
	path := s.launcherPath(r, launcherSpecName)
	if err := process.ReadJSON(path+".started", &started, 16384); err != nil {
		if !os.IsNotExist(err) || process.ClaimRunner(path, process.Started{Revoked: true}) != nil {
			return r, false
		}
	} else if s.launcherState(r) != "dead" {
		// A live launcher may have won the marker since our first read.
		return r, false
	}
	// The receipt may have been installed between the first read and death.
	return s.observeLauncher(r), true
}

// Called only after the existing ownership-file revocation won and neither
// the original launcher nor the runner is alive. Resource settlement remains
// the existing scope/claim owner's responsibility.
func (s *Service) finalizeLauncherFailure(r Record) bool {
	if r.LaunchResult == nil {
		return false
	}
	next, err := s.update(r.ID, func(next *Record) bool {
		if next.Status != "running" || next.LaunchResult == nil {
			return false
		}
		next.ResultSource = "launcher"
		return true
	})
	if err != nil || next.Status != "running" || next.LaunchResult == nil {
		return false
	}
	result := process.Result{
		ExitCode:     next.LaunchResult.ExitCode,
		Signal:       next.LaunchResult.Signal,
		FinishedAtMS: next.LaunchResult.FinishedAtMS,
		Quiescent:    true,
	}
	return s.finalize(r.ID, &result, "launch_failed") == nil
}

func (s *Service) renderedPaths(r Record) [2]string {
	names := [2]string{"stdout.log", "stderr.log"}
	if r.ResultSource == "launcher" {
		names = [2]string{launcherStdoutName, launcherStderrName}
	}
	dir := s.outputDir(r)
	return [2]string{filepath.Join(dir, names[0]), filepath.Join(dir, names[1])}
}

func (s *Service) capturedBytes(r Record) (int64, int64) {
	if r.ResultSource == "launcher" {
		paths := s.renderedPaths(r)
		return fileSize(paths[0]), fileSize(paths[1])
	}
	return fileSize(r.StdoutPath), fileSize(r.StderrPath)
}

func (s *Service) launcherMetadata(r Record, fields map[string]any) {
	if r.ResultSource != "" {
		fields["result_source"] = r.ResultSource
	}
	if result := r.LaunchResult; result != nil {
		var signal any
		if result.Signal != nil {
			signal = nullable(*result.Signal)
		}
		fields["launcher"] = map[string]any{
			"exit_code":        exitValue(result.ExitCode),
			"signal":           signal,
			"error":            result.Error,
			"finished_at_ms":   result.FinishedAtMS,
			"output_truncated": result.OutputTruncated,
		}
	}
	if r.LauncherVersion != 0 && !r.OutputExpired {
		prefix := "$CODEXPRO_JOB_OUTPUT_DIR/" + r.ID + "/"
		fields["launcher_output_files"] = map[string]any{
			"stdout": prefix + launcherStdoutName,
			"stderr": prefix + launcherStderrName,
		}
	}
}

func (s *Service) launcherStoredBytes(r Record) int64 {
	if r.LauncherVersion == 0 {
		return 0
	}
	var size int64
	for _, name := range []string{launcherSpecName, launcherSpecName + ".started", launcherResultName, launcherStdoutName, launcherStderrName} {
		size += fileSize(s.launcherPath(r, name))
	}
	return size
}

func (s *Service) launcherWaitError(r Record, o startOptions, err error) {
	if err != nil && r.LauncherVersion != 0 {
		s.rememberLaunchError(r, o, fmt.Errorf("launcher capture process failed: %w", err))
	}
	s.notify()
}

// A completed payload can leave a wrapper holding diagnostic capture open.
// After the existing stop grace, settle its original execution owner first and
// signal only the separately identified launcher group. A result's presence
// keeps this path from falling back to signalling a stale payload process ID.
func (s *Service) stopFinishedLauncher(ctx context.Context, r Record, started process.Started, scope *process.ScopeIdentity) bool {
	if r.LauncherVersion != process.LaunchSpecVersion {
		return false
	}
	var result process.Result
	if process.ReadJSON(r.ResultPath, &result, 16384) != nil || result.Version != 2 || result.FinishedAtMS <= 0 {
		return false
	}
	if r.ScopeUnit != "" {
		if !process.ValidScopeIdentity(scope, r.ScopeUnit+".scope") || !process.SettleScope(ctx, scope) {
			return true
		}
	} else if !result.Quiescent {
		return true
	}
	if started.PID <= 0 || started.ProcessIdentity == "" ||
		process.ObserveProcess(started.PID, started.ProcessIdentity) != "dead" {
		return true
	}
	var launcher process.Started
	if process.ReadJSON(s.launcherPath(r, launcherSpecName)+".started", &launcher, 16384) != nil ||
		launcher.Revoked || launcher.PID <= 0 || launcher.PID == os.Getpid() || launcher.ProcessIdentity == "" ||
		r.PID > 0 && (r.PID != launcher.PID || r.ProcessIdentity != "" && r.ProcessIdentity != launcher.ProcessIdentity) ||
		process.ObserveProcess(launcher.PID, launcher.ProcessIdentity) != "active" {
		return true
	}
	group, err := syscall.Getpgid(launcher.PID)
	if err == nil && group == launcher.PID &&
		process.ObserveProcess(launcher.PID, launcher.ProcessIdentity) == "active" {
		_ = process.SignalGroup(launcher.PID, syscall.SIGKILL)
	}
	return true
}
