package process

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func requested(path string) string {
	f, e := os.Open(path)
	if e != nil {
		return ""
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 128))
	if e != nil {
		return ""
	}
	s := string(b)
	switch s {
	case "stopped", "timeout", "output_limit":
		return s
	}
	return ""
}

func publish(spec Spec, result Result) error {
	result.Version = 2
	result.FinishedAtMS = time.Now().UnixMilli()
	if e := WriteJSON(spec.Result, result); e != nil {
		return e
	}
	code := 1
	if result.ExitCode != nil {
		code = *result.ExitCode
	}
	return AtomicWrite(spec.Exit, []byte(strconv.Itoa(code)), 0600)
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// RunRunner is dispatched by main before configuration/HTTP startup. It must run
// in its own process, not a request goroutine: accepted jobs survive server exit.
func RunRunner(args []string) error {
	if len(args) != 1 {
		return errors.New("runner requires one private specification path")
	}
	specPath, e := filepath.Abs(args[0])
	if e != nil {
		return e
	}
	var spec Spec
	if e = ReadJSON(specPath, &spec, 4*1024*1024); e != nil {
		return e
	}
	if e = spec.Validate(specPath); e != nil {
		return e
	}
	startedAt := time.Now()
	ctx := context.Background()
	var identity *ScopeIdentity
	if spec.ScopeUnit != "" {
		identity, e = CaptureScopeIdentity(ctx, spec.ScopeUnit)
		if e != nil {
			return e
		}
	}
	cgroup, _ := SelfCgroup()
	started := Started{PID: os.Getpid(), ProcessIdentity: ProcessIdentity(os.Getpid()), Cgroup: cgroup, ScopeIdentity: identity}
	b, e := json.Marshal(started)
	if e != nil {
		return e
	}
	if e = ExclusiveWrite(specPath+".started", b, 0600); e != nil {
		return e
	}
	expired := func() bool {
		return time.Since(startedAt) >= time.Duration(spec.TimeoutMS)*time.Millisecond || time.Now().UnixMilli() >= spec.Deadline
	}
	granted := false
	for time.Since(startedAt) < 10*time.Second && !expired() && requested(spec.Control) == "" {
		b, e := os.ReadFile(spec.Grant)
		if e == nil && len(b) == len(spec.Nonce) && subtle.ConstantTimeCompare(b, []byte(spec.Nonce)) == 1 {
			granted = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !granted || expired() || requested(spec.Control) != "" {
		reason := requested(spec.Control)
		if reason == "" {
			if expired() {
				reason = "timeout"
			} else {
				reason = "lost"
			}
		}
		return publish(spec, Result{StopReason: &reason, Quiescent: true, Cgroup: cgroup, ScopeIdentity: identity})
	}

	// Pin launch to this thread so the payload inherits the reduced scheduling
	// priority even if the runtime has already created other worker threads.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if spec.Nice > 0 {
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, min(19, spec.Nice))
	}
	raw := make([]*os.File, 2)
	writers := make([]*OutputWriter, 2)
	budget := NewRenderBudget(spec.Limit * 2)
	for i, p := range []string{spec.Stdout, spec.Stderr} {
		raw[i], e = os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
		if e != nil {
			for _, f := range raw {
				if f != nil {
					_ = f.Close()
				}
			}
			return e
		}
		name := "stdout.log"
		if i == 1 {
			name = "stderr.log"
		}
		writers[i], e = NewOutputWriter(filepath.Join(spec.OutputDir, name), spec.PathRedactions, budget)
		if e != nil {
			for _, f := range raw {
				if f != nil {
					_ = f.Close()
				}
			}
			for _, w := range writers {
				if w != nil {
					_ = w.Finish()
				}
			}
			return e
		}
	}
	defer func() {
		for _, w := range writers {
			_ = w.Finish()
		}
		for _, f := range raw {
			_ = f.Close()
		}
	}()
	pipes := make([][2]*os.File, 2)
	for i := range pipes {
		pipes[i][0], pipes[i][1], e = os.Pipe()
		if e != nil {
			for _, p := range pipes {
				for _, f := range p {
					if f != nil {
						_ = f.Close()
					}
				}
			}
			return e
		}
	}
	defer func() {
		for _, p := range pipes {
			for _, f := range p {
				_ = f.Close()
			}
		}
	}()
	bash := "/bin/bash"
	if _, e := os.Stat(bash); e != nil {
		bash = "bash"
	}
	cmd := exec.Command(bash, "-lc", spec.Command)
	cmd.Dir = spec.Cwd
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = pipes[0][1]
	cmd.Stderr = pipes[1][1]
	if e = cmd.Start(); e != nil {
		reason := "lost"
		return publish(spec, Result{StopReason: &reason, Quiescent: true, Cgroup: cgroup, ScopeIdentity: identity})
	}
	pid := cmd.Process.Pid
	started.CommandPID = pid
	started.CommandIdentity = ProcessIdentity(pid)
	if e = WriteJSON(specPath+".started", started); e != nil {
		_ = SignalGroup(pid, syscall.SIGKILL)
		_ = cmd.Wait()
		reason := "lost"
		return publish(spec, Result{StopReason: &reason, Quiescent: !GroupAlive(pid), Cgroup: cgroup, ScopeIdentity: identity, CommandPID: pid})
	}
	for _, p := range pipes {
		_ = p[1].Close()
	}
	limitEvents := make(chan string, 1)
	var captureMu sync.Mutex
	var captured int64
	var readers sync.WaitGroup
	for i := range pipes {
		readers.Add(1)
		go func(i int) {
			defer readers.Done()
			buffer := make([]byte, 32*1024)
			for {
				n, err := pipes[i][0].Read(buffer)
				if n > 0 {
					captureMu.Lock()
					remaining := max(int64(0), spec.Limit-captured)
					kept := min(n, int(remaining))
					var writeErr error
					if kept > 0 {
						captured += int64(kept)
						_, writeErr = raw[i].Write(buffer[:kept])
						if writeErr == nil {
							_, writeErr = writers[i].Write(buffer[:kept])
						}
					}
					if kept < n || budget.Exhausted() {
						select {
						case limitEvents <- "output_limit":
						default:
						}
					}
					if writeErr != nil {
						select {
						case limitEvents <- "lost":
						default:
						}
					}
					captureMu.Unlock()
				}
				if err != nil {
					return
				}
			}
		}(i)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var reason string
	var escalation *time.Timer
	var escalationC <-chan time.Time
	stop := func(why string) {
		if reason != "" {
			return
		}
		reason = why
		_ = SignalGroup(pid, syscall.SIGTERM)
		escalation = time.NewTimer(1500 * time.Millisecond)
		escalationC = escalation.C
	}
	finished := false
	for !finished {
		select {
		case <-waited:
			finished = true
		case why := <-limitEvents:
			stop(why)
		case <-signals:
			stop("stopped")
		case <-escalationC:
			_ = SignalGroup(pid, syscall.SIGKILL)
			escalationC = nil
		case <-ticker.C:
			if expired() {
				stop("timeout")
			}
			if why := requested(spec.Control); why != "" {
				stop(why)
			}
		}
	}
	if escalation != nil {
		escalation.Stop()
	}
	quiet := DrainGroup(ctx, pid)
	done := make(chan struct{})
	go func() { readers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		for _, p := range pipes {
			_ = p[0].Close()
		}
		<-done
	}
	if reason == "" {
		select {
		case reason = <-limitEvents:
		default:
		}
	}
	// Stop annotations are deliberately outside the raw payload budget, as in TS.
	if reason != "" {
		message := fmt.Sprintf("\n[codexpro] Command stopped: %s.\n", reason)
		if reason == "timeout" {
			message = fmt.Sprintf("\n[codexpro] Command timed out after %d ms.\n", spec.TimeoutMS)
		}
		_, _ = io.WriteString(raw[1], message)
		_, _ = writers[1].Write([]byte(message))
	}
	for _, w := range writers {
		if e = w.Finish(); e != nil && reason == "" {
			reason = "lost"
		}
	}
	for _, f := range raw {
		if e = f.Sync(); e != nil && reason == "" {
			reason = "lost"
		}
		_ = f.Close()
	}
	var code *int
	var sig *string
	if cmd.ProcessState != nil {
		if n := cmd.ProcessState.ExitCode(); n >= 0 {
			code = &n
		}
		if stat, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && stat.Signaled() {
			name := signalName(stat.Signal())
			sig = &name
		}
	}
	return publish(spec, Result{ExitCode: code, Signal: sig, StopReason: stringPtr(reason), Quiescent: quiet, Cgroup: cgroup, ScopeIdentity: identity, CommandPID: pid})
}

func RestrictedEnv(inherit bool) []string {
	current := map[string]string{}
	for _, entry := range os.Environ() {
		if k, v, ok := strings.Cut(entry, "="); ok {
			current[k] = v
		}
	}
	if inherit {
		current["NO_COLOR"] = "1"
		if _, ok := current["CI"]; !ok {
			current["CI"] = "1"
		}
		return envSlice(current)
	}
	usable := func(p string) string {
		if !filepath.IsAbs(p) {
			return ""
		}
		s, e := os.Stat(p)
		if e != nil || !s.IsDir() {
			return ""
		}
		return p
	}
	home := usable(current["USERPROFILE"])
	if home == "" {
		home = usable(current["HOME"])
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	tmp := usable(current["TMPDIR"])
	if tmp == "" {
		tmp = usable(current["TMP"])
	}
	if tmp == "" {
		tmp = os.TempDir()
	}
	path := current["PATH"]
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	user := current["USER"]
	if user == "" {
		user = current["USERNAME"]
	}
	shell := current["SHELL"]
	if shell == "" {
		shell = "/bin/bash"
	}
	env := map[string]string{"PATH": path, "HOME": home, "USER": user, "SHELL": shell, "TMPDIR": tmp, "TERM": "dumb", "NO_COLOR": "1", "CI": "1"}
	if v := current["AGENTMAIL_TOKEN_FILE"]; v != "" {
		env["AGENTMAIL_TOKEN_FILE"] = v
	}
	return envSlice(env)
}

func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func WithEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, s := range env {
		if !strings.HasPrefix(s, prefix) {
			out = append(out, s)
		}
	}
	return append(out, prefix+value)
}
