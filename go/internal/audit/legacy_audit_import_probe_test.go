package audit

// This optional lane imports only a verified private legacy journal into a new
// private directory. It keeps the resulting saturated store for side-by-side
// performance runs, but starts no server and touches no live config/state.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func legacyAuditWithin(root, value string) bool {
	rel, err := filepath.Rel(root, value)
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
func legacyAuditPrivate(name string) (string, error) {
	value, err := filepath.EvalSymlinks(name)
	if err != nil {
		return "", err
	}
	value, err = filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if err = legacyAuditAllowed(value); err != nil {
		return "", err
	}
	return value, nil
}
func legacyAuditAllowed(value string) error {
	homes := []string{}
	if h, e := os.UserHomeDir(); e == nil {
		homes = append(homes, h)
	}
	if u, e := user.Current(); e == nil {
		homes = append(homes, u.HomeDir)
	}
	for _, home := range homes {
		for _, suffix := range []string{".codexpro", filepath.Join(".config", "codexpro")} {
			blocked := filepath.Join(home, suffix)
			if legacyAuditWithin(blocked, value) {
				return errors.New("protected live state refused")
			}
			if real, e := filepath.EvalSymlinks(blocked); e == nil && legacyAuditWithin(real, value) {
				return errors.New("protected live state alias refused")
			}
		}
	}
	return nil
}
func legacyAuditHash(name string, maximum int64) (string, int64, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !st.Mode().IsRegular() || st.Size() > maximum {
		return "", 0, errors.New("audit proof input exceeds its regular-file byte bound")
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	after, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !os.SameFile(st, after) || st.Size() != after.Size() || !st.ModTime().Equal(after.ModTime()) {
		return "", 0, errors.New("audit proof input changed during hash")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func legacyAuditCanonical(value any) []byte {
	data, _ := json.Marshal(value)
	var normalized any
	if json.Unmarshal(data, &normalized) != nil {
		return nil
	}
	result, _ := json.Marshal(normalized)
	return result
}
func legacyAuditRead(name string) ([]map[string]any, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 131072), 131073)
	values := []map[string]any{}
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var value map[string]any
		if err = json.Unmarshal(scanner.Bytes(), &value); err != nil {
			return nil, err
		}
		if value == nil {
			return nil, errors.New("legacy action is not an object")
		}
		values = append(values, value)
		if len(values) > 20000 {
			return nil, errors.New("legacy audit action budget exceeded")
		}
	}
	return values, scanner.Err()
}
func legacyAuditStatus(ctx context.Context, j *Journal) (map[string]any, error) {
	r, err := j.Call(ctx, wire.Scope{PrincipalID: "migration_probe"}, "activity_status", map[string]any{})
	if err != nil {
		return nil, err
	}
	if r.IsError {
		return nil, errors.New("audit status returned a tool error")
	}
	return r.StructuredContent, nil
}

func TestLegacyAuditImportProbe(t *testing.T) {
	sourceName := os.Getenv("SP6_AUDIT_SOURCE")
	if sourceName == "" {
		t.Skip("explicit private legacy audit snapshot not supplied")
	}
	indexName, destinationName := os.Getenv("SP6_AUDIT_INDEX"), os.Getenv("SP6_AUDIT_DEST")
	if indexName == "" || destinationName == "" {
		t.Fatal("SP6_AUDIT_INDEX and SP6_AUDIT_DEST are required")
	}
	source, err := legacyAuditPrivate(sourceName)
	if err != nil {
		t.Fatal(err)
	}
	index, err := legacyAuditPrivate(indexName)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{sourceName, indexName} {
		if st, e := os.Lstat(name); e != nil || st.Mode()&os.ModeSymlink != 0 {
			t.Fatal("private snapshot inputs must be real regular files")
		}
	}
	sourceHash, sourceBytes, err := legacyAuditHash(source, 64*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	indexHash, _, err := legacyAuditHash(index, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := filepath.Abs(destinationName)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := legacyAuditPrivate(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	if parent != filepath.Dir(destination) {
		t.Fatal("destination parent is not a real private path")
	}
	if err = legacyAuditAllowed(destination); err != nil {
		t.Fatal(err)
	}
	if legacyAuditWithin(filepath.Dir(source), destination) || legacyAuditWithin(filepath.Dir(index), destination) {
		t.Fatal("use a destination separate from the source snapshot directory")
	}
	if _, err = os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatal("audit import destination already exists")
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	values, err := legacyAuditRead(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) == 0 {
		t.Fatal("legacy audit snapshot is empty")
	}
	byID := map[string]map[string]any{}
	buckets := map[string]int{}
	var previous int64
	encodedBytes := 0
	for _, value := range values {
		id, _ := value["action_id"].(string)
		sequence, ok := value["sequence"].(float64)
		if id == "" || byID[id] != nil || !ok || int64(sequence) <= previous || float64(int64(sequence)) != sequence || value["schema_version"] != "1.0" {
			t.Fatal("snapshot has an invalid, duplicate or unordered action")
		}
		previous = int64(sequence)
		byID[id] = value
		scope, _ := value["audit_scope"].(string)
		tool, _ := value["tool_name"].(string)
		project, _ := value["project_id"].(string)
		bucket := "unattributed"
		if scope == "server" {
			bucket = "server"
		} else if scope != "unattributed" && !(strings.HasPrefix(tool, "work_") && scope == "") && project != "" {
			bucket = "project:" + project
		}
		buckets[bucket]++
		if buckets[bucket] > 350 {
			t.Fatal("snapshot exceeds the configured 350-action retention bucket; do not silently prune migration evidence")
		}
		encodedBytes += len(legacyAuditCanonical(value)) + 1
	}
	if encodedBytes > 8388608 {
		t.Fatal("snapshot exceeds the 8 MiB destination budget after JSON serialization")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	options := Options{Directory: destination, Enabled: true, MaxBytes: 8388608, RetainActions: 350, MaxOutputBytes: 120000, Redact: wire.Redact}
	j, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if j != nil {
			j.Close()
		}
	}()
	started := time.Now()
	imported, err := j.ImportLegacy(ctx, source, index)
	if err != nil {
		t.Fatal(err)
	}
	importMS := float64(time.Since(started).Microseconds()) / 1000
	if !imported.Imported || imported.SourceSHA256 != sourceHash || imported.ActionCount != len(values) || imported.LatestSequence < previous {
		t.Fatal("legacy import did not preserve every action and sequence")
	}
	before, err := legacyAuditStatus(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j = nil
	files := []string{}
	for _, pattern := range []string{"segment-*.jsonl", "compact-*.jsonl"} {
		matched, err := filepath.Glob(filepath.Join(destination, pattern))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matched...)
	}
	sort.Strings(files)
	stored := map[string]map[string]any{}
	for _, file := range files {
		st, err := os.Lstat(file)
		if err != nil || !st.Mode().IsRegular() {
			t.Fatal("private audit segment is not a regular file")
		}
		actions, err := legacyAuditRead(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range actions {
			id, _ := value["action_id"].(string)
			if stored[id] != nil {
				t.Fatal("import stored duplicate action identity")
			}
			stored[id] = value
		}
	}
	if len(stored) != len(values) {
		t.Fatal("physical segments do not preserve every source action")
	}
	for id, value := range byID {
		if !bytes.Equal(legacyAuditCanonical(value), legacyAuditCanonical(stored[id])) {
			t.Fatalf("stored action differs from original private record: %s", id)
		}
	}
	j, err = Open(options)
	if err != nil {
		t.Fatal(err)
	}
	after, err := legacyAuditStatus(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacyAuditCanonical(before), legacyAuditCanonical(after)) {
		t.Fatal("audit status changed across restart without a write")
	}
	readStarted := time.Now()
	for _, value := range values {
		id := value["action_id"].(string)
		result, err := j.Call(ctx, wire.Scope{PrincipalID: "migration_probe"}, "activity_get", map[string]any{"action_id": id})
		if err != nil || result.IsError {
			t.Fatalf("imported action unavailable from public reader: %s", id)
		}
		public := map[string]any{}
		for key, item := range value {
			if key != "dashboard_metadata" {
				public[key] = item
			}
		}
		if !bytes.Equal(legacyAuditCanonical(public), legacyAuditCanonical(result.StructuredContent["action"])) {
			t.Fatalf("imported public action DTO differs: %s", id)
		}
	}
	readMS := float64(time.Since(readStarted).Microseconds()) / 1000
	duplicate, err := j.ImportLegacy(ctx, source, index)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Imported || duplicate.SourceSHA256 != sourceHash || duplicate.ActionCount != imported.ActionCount || duplicate.LatestSequence != imported.LatestSequence || duplicate.GapDetected != imported.GapDetected {
		t.Fatal("durable import receipt did not survive restart")
	}
	final, err := legacyAuditStatus(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacyAuditCanonical(after), legacyAuditCanonical(final)) {
		t.Fatal("read-only inspection or import replay changed audit status")
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j = nil
	if hash, _, err := legacyAuditHash(source, 64*1024*1024); err != nil || hash != sourceHash {
		t.Fatal("original private JSONL snapshot changed")
	}
	if hash, _, err := legacyAuditHash(index, 1024*1024); err != nil || hash != indexHash {
		t.Fatal("original private retention sidecar changed")
	}
	report := map[string]any{"schema_version": 1, "source_commit": "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a",
		"lane": "legacy-audit-import-and-restart", "passed": true, "source_sha256": sourceHash, "source_bytes": sourceBytes, "sidecar_sha256": indexHash,
		"actions": len(values), "public_action_dtos_verified": len(values), "full_private_record_values_preserved": true, "source_unchanged": true,
		"restart_status_unchanged": true, "durable_import_replay_verified": true, "import": imported, "status": final,
		"configured_max_bytes": 8388608, "configured_retain_actions": 350, "configured_max_output_bytes": 120000,
		"import_ms": importMS, "all_public_gets_ms": readMS,
		"scope": "Exact private legacy capture import; no server, live state access, process adoption or cutover. Directory remains available as a saturated private performance fixture."}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(destination, "import-proof.json")
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.Write(append(encoded, '\n')); err != nil {
		t.Fatal(err)
	}
	if err = f.Sync(); err != nil {
		t.Fatal(err)
	}
	t.Logf("imported=%d public_dtos=%d source_unchanged=true restart=true replay=true report=%s", len(values), len(values), filepath.Base(output))
}
