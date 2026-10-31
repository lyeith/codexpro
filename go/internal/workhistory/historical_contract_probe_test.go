package workhistory

// This opt-in offline differential probe is exercised only with an explicitly
// prepared, disposable normalized archive. Ordinary unit tests never open user
// state. The TypeScript reference has already validated/defaulted each request.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type contractPlan struct {
	SchemaVersion  int    `json:"schema_version"`
	SourceCommit   string `json:"source_commit"`
	DisposableCopy bool   `json:"disposable_copy"`
	Clock          string `json:"clock"`
	PacketBytes    int    `json:"packet_bytes"`
	ContextDir     string `json:"context_dir"`
	SourceArchive  string `json:"source_archive"`
	Archive        struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Bytes  int64  `json:"bytes"`
	} `json:"archive"`
}
type contractRequest struct {
	CaseID          string         `json:"case_id"`
	PrincipalID     string         `json:"principal_id"`
	Arguments       map[string]any `json:"arguments"`
	ParsedArguments map[string]any `json:"parsed_arguments"`
}

func contractWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}
func contractPrivatePath(file string) (string, error) {
	resolved, err := filepath.EvalSymlinks(file)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	homes := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		homes = append(homes, home)
	}
	if account, err := user.Current(); err == nil {
		homes = append(homes, account.HomeDir)
	}
	for _, home := range homes {
		for _, suffix := range []string{".codexpro", filepath.Join(".config", "codexpro")} {
			blocked := filepath.Join(home, suffix)
			if contractWithin(blocked, resolved) {
				return "", errors.New("protected live state path refused")
			}
			if real, err := filepath.EvalSymlinks(blocked); err == nil && contractWithin(real, resolved) {
				return "", errors.New("protected live state alias refused")
			}
		}
	}
	return resolved, nil
}
func contractDigest(file string) (string, int64, error) {
	handle, err := os.Open(file)
	if err != nil {
		return "", 0, err
	}
	defer handle.Close()
	before, err := handle.Stat()
	if err != nil {
		return "", 0, err
	}
	if !before.Mode().IsRegular() || before.Size() > 1024*1024*1024 {
		return "", 0, errors.New("archive must be a bounded regular file")
	}
	hash := sha256.New()
	count, err := io.Copy(hash, handle)
	if err != nil {
		return "", 0, err
	}
	after, err := handle.Stat()
	if err != nil {
		return "", 0, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", 0, errors.New("archive changed during verification")
	}
	return hex.EncodeToString(hash.Sum(nil)), count, nil
}
func contractWriteJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(value)
	if err == nil {
		err = file.Sync()
	}
	closed := file.Close()
	if err != nil {
		return err
	}
	return closed
}

// TestHistoricalContractProbe is an opt-in read-only compatibility lane.
// Set SP6_WORK_PLAN, SP6_WORK_REFERENCE and SP6_WORK_CANDIDATE to private files.
// It never invokes a coordinator, session, process, Git, source inspection,
// audit append, or any other operation that can execute archived work.
func TestHistoricalContractProbe(t *testing.T) {
	planName := os.Getenv("SP6_WORK_PLAN")
	if planName == "" {
		t.Skip("explicit disposable work archive fixture not supplied")
	}
	referenceName, outputName := os.Getenv("SP6_WORK_REFERENCE"), os.Getenv("SP6_WORK_CANDIDATE")
	if referenceName == "" || outputName == "" {
		t.Fatal("reference and candidate evidence paths are required")
	}
	planPath, err := contractPrivatePath(planName)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(planPath)
	info, err := os.Stat(planPath)
	if err != nil || info.Size() > 1024*1024 {
		t.Fatal("invalid plan file")
	}
	planData, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	var plan contractPlan
	if err = json.Unmarshal(planData, &plan); err != nil {
		t.Fatal("invalid plan JSON")
	}
	if plan.SchemaVersion != 1 || plan.SourceCommit != "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a" || !plan.DisposableCopy {
		t.Fatal("plan is not an approved disposable archive")
	}
	clock, err := time.Parse(time.RFC3339Nano, plan.Clock)
	if err != nil || clock.UTC().Format("2006-01-02T15:04:05.000Z") != plan.Clock {
		t.Fatal("invalid fixed archive clock")
	}
	if plan.PacketBytes < 4096 || plan.PacketBytes > 65536 || plan.ContextDir == "" {
		t.Fatal("invalid archive rendering options")
	}
	archive, err := contractPrivatePath(plan.Archive.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !contractWithin(root, archive) {
		t.Fatal("archive escapes its explicit fixture")
	}
	link, err := os.Lstat(plan.Archive.Path)
	if err != nil || link.Mode()&os.ModeSymlink != 0 {
		t.Fatal("archive must not be a symlink")
	}
	if plan.SourceArchive != "" {
		original, err := filepath.EvalSymlinks(plan.SourceArchive)
		if err != nil {
			t.Fatal(err)
		}
		if original == archive {
			t.Fatal("probe requires a disposable copy, not the published archive")
		}
	}
	for _, suffix := range []string{"-wal", "-journal"} {
		if st, err := os.Stat(archive + suffix); err == nil && st.Size() > 0 {
			t.Fatal("archive normalization is incomplete")
		} else if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	before, bytes, err := contractDigest(archive)
	if err != nil {
		t.Fatal(err)
	}
	if before != plan.Archive.SHA256 || bytes != plan.Archive.Bytes {
		t.Fatal("archive digest differs from its capture")
	}
	reference, err := contractPrivatePath(referenceName)
	if err != nil {
		t.Fatal(err)
	}
	if !contractWithin(root, reference) {
		t.Fatal("reference evidence escapes its fixture")
	}
	referenceInfo, err := os.Stat(reference)
	if err != nil || referenceInfo.Size() > 64*1024*1024 {
		t.Fatal("reference evidence exceeds 64 MiB")
	}
	output, err := filepath.Abs(outputName)
	if err != nil {
		t.Fatal(err)
	}
	outputParent, err := contractPrivatePath(filepath.Dir(output))
	if err != nil {
		t.Fatal(err)
	}
	if outputParent != filepath.Dir(output) || !contractWithin(root, output) {
		t.Fatal("candidate output escapes its private real directory tree")
	}
	if _, err = os.Lstat(output); !os.IsNotExist(err) {
		t.Fatal("candidate output already exists")
	}
	source, err := os.Open(reference)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	history, err := Open(archive, Options{Clock: func() time.Time { return clock }, PacketBytes: plan.PacketBytes, ContextDir: plan.ContextDir})
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	seen := map[string]bool{}
	count, failures, totalBytes := 0, 0, 0
	for scanner.Scan() {
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		var request contractRequest
		if err = json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatal("invalid reference record")
		}
		if request.CaseID == "" || seen[request.CaseID] || request.PrincipalID == "" || request.ParsedArguments == nil {
			t.Fatal("reference cases lack validated identities/arguments")
		}
		seen[request.CaseID] = true
		count++
		if count > 1024 {
			t.Fatal("archive case budget exceeded")
		}
		row := map[string]any{"case_id": request.CaseID, "principal_id": request.PrincipalID, "arguments": request.Arguments, "parsed_arguments": request.ParsedArguments}
		result, callError := history.Call(context.Background(), wire.Scope{PrincipalID: request.PrincipalID, RequestID: request.CaseID, StartedAt: clock}, "work_status", request.ParsedArguments)
		if callError != nil {
			failures++
			code, message := "candidate_internal", callError.Error()
			var typed *wire.Error
			if errors.As(callError, &typed) {
				code, message = typed.Code, typed.Message
			}
			row["error"] = map[string]any{"code": code, "message": message}
		} else {
			// Preserve the logical raw textResult envelope, including its empty
			// metadata object, before the main engine's common tool-title tags.
			value := map[string]any{"_meta": result.Meta, "content": result.Content, "structuredContent": result.StructuredContent}
			if result.IsError {
				value["isError"] = true
			}
			row["result"] = value
		}
		encoded := jsonData(row)
		totalBytes += len(encoded) + 1
		if totalBytes > 64*1024*1024 {
			t.Fatal("candidate evidence exceeded 64 MiB")
		}
		if _, err = target.Write(append(encoded, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("reference case stream is empty")
	}
	if err = target.Sync(); err != nil {
		t.Fatal(err)
	}
	if err = target.Close(); err != nil {
		t.Fatal(err)
	}
	if err = history.Close(); err != nil {
		t.Fatal(err)
	}
	after, afterBytes, err := contractDigest(archive)
	if err != nil {
		t.Fatal(err)
	}
	if before != after || bytes != afterBytes {
		t.Fatal("read-only candidate changed the archive")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err = os.Stat(archive + suffix); !os.IsNotExist(err) {
			t.Fatal("candidate unexpectedly created an archive sidecar")
		}
	}
	outputHash, _, err := contractDigest(output)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"schema_version": 1, "source_commit": plan.SourceCommit, "cases": count, "expected_or_observed_errors": failures, "archive_unchanged": true, "archive_sha256": before, "archive_bytes": bytes, "fixed_clock": plan.Clock, "output_sha256": outputHash, "output_bytes": totalBytes, "scope": "Pure archived work_status DTOs; no transport, audit append, reconciliation, session, or execution."}
	if err = contractWriteJSON(output+".manifest.json", manifest); err != nil {
		t.Fatal(err)
	}
	t.Logf("archived work_status cases=%d errors=%d archive_unchanged=true evidence=%s", count, failures, filepath.Base(output))
}
