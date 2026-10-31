// Package process contains the detached runner and exact process-owner protocol.
// It has no dependency on the HTTP server or its request lifetime.
package process

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type ResourceClaim struct {
	Version        int    `json:"version"`
	Helper         string `json:"helper"`
	RunID          string `json:"run_id"`
	RegistrationID string `json:"registration_id"`
}

type ScopeIdentity struct {
	Version      int    `json:"version"`
	Unit         string `json:"unit"`
	BootID       string `json:"boot_id"`
	InvocationID string `json:"invocation_id"`
	Cgroup       string `json:"cgroup"`
}

type PathRedaction [2]string

type Spec struct {
	Version        int             `json:"version"`
	Command        string          `json:"command"`
	Cwd            string          `json:"cwd"`
	Stdout         string          `json:"stdout"`
	Stderr         string          `json:"stderr"`
	Exit           string          `json:"exit"`
	Result         string          `json:"result"`
	Control        string          `json:"control"`
	OutputDir      string          `json:"outputDir"`
	Grant          string          `json:"grant"`
	Nonce          string          `json:"nonce"`
	ScopeUnit      string          `json:"scopeUnit,omitempty"`
	Deadline       int64           `json:"deadline"`
	TimeoutMS      int64           `json:"timeoutMs"`
	Limit          int64           `json:"limit"`
	Nice           int             `json:"nice"`
	PathRedactions []PathRedaction `json:"pathRedactions"`
}

type Started struct {
	PID             int            `json:"pid"`
	ProcessIdentity string         `json:"process_identity,omitempty"`
	CommandPID      int            `json:"command_pid,omitempty"`
	CommandIdentity string         `json:"command_identity,omitempty"`
	Cgroup          string         `json:"cgroup,omitempty"`
	ScopeIdentity   *ScopeIdentity `json:"scope_identity,omitempty"`
}

type Result struct {
	Version       int            `json:"version"`
	ExitCode      *int           `json:"exit_code"`
	Signal        *string        `json:"signal"`
	StopReason    *string        `json:"stop_reason"`
	Quiescent     bool           `json:"quiescent"`
	Cgroup        string         `json:"cgroup,omitempty"`
	ScopeIdentity *ScopeIdentity `json:"scope_identity,omitempty"`
	FinishedAtMS  int64          `json:"finished_at_ms"`
	CommandPID    int            `json:"command_pid,omitempty"`
}

func RandomHex(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// AtomicWrite makes acknowledgement a durability boundary, including the rename.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".codexpro-write-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	return SyncDir(dir)
}

func ExclusiveWrite(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return SyncDir(filepath.Dir(path))
}

func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func WriteJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return AtomicWrite(path, b, 0600)
}

func ReadJSON(path string, v any, maxBytes int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(b)) > maxBytes {
		return errors.New("process metadata exceeds its byte limit")
	}
	return json.Unmarshal(b, v)
}

func Within(root, path string) bool {
	r, err := filepath.Rel(root, path)
	return err == nil && (r == "." || (r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))))
}

func (s Spec) Validate(specPath string) error {
	if s.Version != 1 || strings.TrimSpace(s.Command) == "" || s.TimeoutMS < 1 || s.Limit < 1 || s.Deadline < 1 {
		return errors.New("invalid job runner specification")
	}
	if len(s.Nonce) != 48 {
		return errors.New("invalid runner grant nonce")
	}
	if _, err := hex.DecodeString(s.Nonce); err != nil {
		return errors.New("invalid runner grant nonce")
	}
	if !filepath.IsAbs(s.Cwd) {
		return errors.New("runner cwd must be absolute")
	}
	base := filepath.Dir(specPath)
	for _, p := range []string{s.Stdout, s.Stderr, s.Exit, s.Result, s.Control, s.OutputDir, s.Grant} {
		if !filepath.IsAbs(p) || !Within(base, p) {
			return fmt.Errorf("runner metadata path escapes its private directory")
		}
	}
	if s.ScopeUnit != "" && !scopeUnitPattern.MatchString(s.ScopeUnit) {
		return errors.New("invalid runner scope")
	}
	return nil
}
