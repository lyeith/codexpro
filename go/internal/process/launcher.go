package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	LaunchSpecVersion   = 1
	LaunchResultVersion = 1

	// LaunchDiagnosticLimit bounds the retained, redacted diagnostics across
	// both streams. Payload output remains owned by the ordinary runner.
	LaunchDiagnosticLimit int64 = 16 * 1024
)

type LaunchSpec struct {
	Version        int             `json:"version"`
	Args           []string        `json:"args"`
	Cwd            string          `json:"cwd"`
	Stdout         string          `json:"stdout"`
	Stderr         string          `json:"stderr"`
	Result         string          `json:"result"`
	Limit          int64           `json:"limit"`
	PathRedactions []PathRedaction `json:"pathRedactions"`
}

// LaunchResult describes the outer launcher only. A runner result remains the
// authority for payload exit; this receipt makes no claim about owner quiescence.
type LaunchResult struct {
	Version         int     `json:"version"`
	ExitCode        *int    `json:"exit_code"`
	Signal          *string `json:"signal"`
	Error           string  `json:"error,omitempty"`
	FinishedAtMS    int64   `json:"finished_at_ms"`
	OutputTruncated bool    `json:"output_truncated,omitempty"`
}

func (s LaunchSpec) Validate(specPath string) error {
	if s.Version != LaunchSpecVersion || len(s.Args) == 0 || s.Args[0] == "" ||
		s.Limit < 1 || s.Limit > LaunchDiagnosticLimit {
		return errors.New("invalid job launcher specification")
	}
	for _, arg := range s.Args {
		if strings.IndexByte(arg, 0) >= 0 || !utf8.ValidString(arg) {
			return errors.New("invalid job launcher argument")
		}
	}
	if !filepath.IsAbs(s.Cwd) {
		return errors.New("launcher cwd must be absolute")
	}
	base := filepath.Dir(specPath)
	seen := map[string]bool{specPath: true, specPath + ".started": true}
	for _, path := range []string{s.Stdout, s.Stderr, s.Result} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
			path == base || !Within(base, path) || seen[path] {
			return errors.New("launcher evidence path escapes or aliases its private directory")
		}
		seen[path] = true
	}
	return nil
}

// launchCapture never stops draining a child because diagnostic storage failed
// or filled. Returning a write error to exec's copier would close the pipe and
// could change an otherwise accepted command's behavior.
type launchCapture struct {
	writer *OutputWriter
	budget *RenderBudget
	err    error
}

func (w *launchCapture) Write(p []byte) (int, error) {
	if w.err == nil && !w.budget.Exhausted() {
		_, w.err = w.writer.Write(p)
	}
	return len(p), nil
}

// LaunchErrorText redacts and bounds errors stored with durable launch evidence.
func LaunchErrorText(err error, paths []PathRedaction) string {
	if err == nil {
		return ""
	}
	message := RedactPaths(RedactSensitiveText(strings.ToValidUTF8(err.Error(), "�")), OrderedPaths(paths))
	const limit = 4096
	if len(message) > limit {
		message = message[:limit]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	return message
}

// RunLauncher owns only diagnostic capture and the wait for the existing launch
// command. It is a separate process so server shutdown cannot close the child's
// diagnostic pipes. It neither admits work nor creates a new scope or claim.
func RunLauncher(args []string) error {
	if len(args) != 1 {
		return errors.New("launcher requires one private specification path")
	}
	specPath, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	specFile, err := os.Open(specPath)
	if err != nil {
		return err
	}
	defer specFile.Close()
	specInfo, err := specFile.Stat()
	if err != nil {
		return err
	}
	var spec LaunchSpec
	if err := readJSON(specFile, &spec, 4*1024*1024); err != nil {
		return err
	}
	if err := spec.Validate(specPath); err != nil {
		return err
	}
	// This marker identifies only the independent diagnostic writer. The
	// actual runner retains its separate specification and ownership marker.
	started := Started{PID: os.Getpid(), ProcessIdentity: ProcessIdentity(os.Getpid())}
	if err := ClaimRunner(specPath, started); err != nil {
		return err
	}
	// A delayed launcher cannot execute from a specification removed or
	// replaced while recovery was resolving the original launch.
	currentSpec, err := os.Stat(specPath)
	if err != nil || !os.SameFile(specInfo, currentSpec) {
		return errors.New("job launcher specification was removed or replaced")
	}
	_ = specFile.Close()

	result := LaunchResult{Version: LaunchResultVersion}
	var issues []error
	addIssue := func(stage string, err error) {
		if err != nil {
			issues = append(issues, fmt.Errorf("%s: %w", stage, err))
		}
	}
	publish := func() error {
		result.Error = LaunchErrorText(errors.Join(issues...), spec.PathRedactions)
		result.FinishedAtMS = time.Now().UnixMilli()
		return WriteJSON(spec.Result, result)
	}
	budget := NewRenderBudget(spec.Limit)
	var captures [2]*launchCapture
	for i, path := range []string{spec.Stdout, spec.Stderr} {
		writer, err := NewOutputWriter(path, spec.PathRedactions, budget)
		if err != nil {
			addIssue("launcher capture setup", err)
			for _, capture := range captures {
				if capture != nil {
					addIssue("launcher capture finish", capture.writer.Finish())
				}
			}
			return publish()
		}
		captures[i] = &launchCapture{writer: writer, budget: budget}
	}

	cmd := exec.Command(spec.Args[0], spec.Args[1:]...)
	cmd.Dir = spec.Cwd
	cmd.Env = os.Environ()
	cmd.Stdout, cmd.Stderr = captures[0], captures[1]
	// A finished wrapper may leave a descendant holding its inherited pipes.
	// Bound only that post-exit drain; the accepted execution has no new timer.
	cmd.WaitDelay = 300 * time.Millisecond
	if err := cmd.Start(); err != nil {
		addIssue("launcher start", err)
	} else {
		waitErr := cmd.Wait()
		addIssue("launcher wait", waitErr)
		result.OutputTruncated = errors.Is(waitErr, exec.ErrWaitDelay)
		if state := cmd.ProcessState; state != nil {
			if code := state.ExitCode(); code >= 0 {
				result.ExitCode = &code
			}
			if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				name := signalName(status.Signal())
				result.Signal = &name
			}
		}
	}
	// Wait joins the stream copiers, including after WaitDelay closes pipes.
	// Each capture's error is now stable and no child can race Finish.
	for _, capture := range captures {
		addIssue("launcher capture", capture.err)
		finishErr := capture.writer.Finish()
		addIssue("launcher capture finish", finishErr)
		result.OutputTruncated = result.OutputTruncated || capture.err != nil || finishErr != nil
	}
	result.OutputTruncated = result.OutputTruncated || budget.Exhausted()
	return publish()
}
