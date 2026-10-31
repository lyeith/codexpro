package workhistory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const runA = "run_12345678aaaaaaaaaaaaaaaa"
const runB = "run_12345678bbbbbbbbbbbbbbbb"
const runOther = "run_99999999cccccccccccccccc"

func fixture(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "work.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	schema := `
 CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 INSERT INTO metadata VALUES('schema','3');
 CREATE TABLE runs(id TEXT PRIMARY KEY,principal TEXT,project TEXT,state TEXT,revision INTEGER,body TEXT);
 CREATE TABLE iterations(id TEXT PRIMARY KEY,run_id TEXT,body TEXT);
 CREATE TABLE operations(id TEXT PRIMARY KEY,run_id TEXT,operation_key TEXT,body TEXT);
 CREATE TABLE documents(id TEXT,run_id TEXT,revision INTEGER,kind TEXT,title TEXT,content TEXT,body TEXT,PRIMARY KEY(id,revision));
 CREATE TABLE events(sequence INTEGER PRIMARY KEY,run_id TEXT,kind TEXT,at TEXT,body TEXT);
 CREATE TABLE work_jobs(id TEXT PRIMARY KEY,run_id TEXT,body TEXT);`
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	encode := func(value any) string {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	put := func(query string, args ...any) {
		if _, err = db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{runA, runB, runOther} {
		principal, project, state := "owner", "alpha", "ready"
		if i == 1 {
			project = "beta"
			state = "blocked"
		}
		if i == 2 {
			principal = "other"
		}
		run := map[string]any{"id": id, "principal_id": principal, "project_id": project, "mode": "manual", "state": state, "title": "Archived task", "objective": strings.Repeat("review objective ", 1000), "scope": "Captured historical context", "revision": 3, "plan_revision": 2, "spec_revision": 1, "generation": 2, "attempt_count": 4, "no_progress_count": 0, "updated_at": "2026-10-01T00:00:00.000Z", "workspace": map[string]any{"id": "ws_history", "root": "/copied/tree", "branch": "archive"}, "todos": []any{map[string]any{"id": "t1", "status": "done"}, map[string]any{"id": "t2", "status": "pending"}}, "acceptance": []any{map[string]any{"id": "a1", "required": true}}}
		if id == runA {
			run["iteration_id"] = "it1"
			run["checkpoint"] = map[string]any{"summary": "Saved handoff", "next_action": "Read archived evidence", "activity_sequence": 37}
		}
		put("INSERT INTO runs VALUES(?,?,?,?,?,?)", id, principal, project, state, 3, encode(run))
	}
	put("INSERT INTO iterations VALUES(?,?,?)", "it1", runA, encode(map[string]any{"id": "it1", "run_id": runA, "principal_id": "owner", "token_hash": "private-session-hash", "clock": map[string]any{"epoch": "private-clock"}, "last_contact_at": "2026-10-01T00:00:00.000Z", "last_progress_at": "2026-09-30T00:00:00.000Z"}))
	put("INSERT INTO operations VALUES(?,?,?,?)", "op1", runA, "k1", encode(map[string]any{"id": "op1", "run_id": runA, "operation_key": "k1", "state": "unknown", "fingerprint": "private-operation-fingerprint", "result": map[string]any{"stdout": "receipt content"}}))
	document := strings.Repeat("one😀雪\n", 500)
	for _, doc := range []map[string]any{
		{"id": "doc_spec", "run_id": runA, "revision": 1, "kind": "spec", "title": "Specification", "content": document, "bytes": len([]byte(document)), "content_hash": "stored-hash", "origin": "request", "created_at": "2026-09-30T00:00:00.000Z", "updated_at": "2026-09-30T00:00:00.000Z"},
		{"id": "doc_memory", "run_id": runA, "revision": 1, "kind": "project_memory", "title": "Memory", "content": "The reusable NEEDLE is retained.", "bytes": 32, "content_hash": "memory-hash", "origin": "request", "updated_at": "2026-09-30T00:00:00.000Z"},
	} {
		put("INSERT INTO documents VALUES(?,?,?,?,?,?,?)", doc["id"], runA, doc["revision"], doc["kind"], doc["title"], doc["content"], encode(doc))
	}
	put("INSERT INTO work_jobs VALUES(?,?,?)", "job_archived", runA, encode(map[string]any{"id": "job_archived", "run_id": runA, "status": "running", "quiescent": false, "started_at": "2026-09-30T00:00:00.000Z", "work": map[string]any{"operation_id": "op1"}, "deadline_ms": 123}))
	put("INSERT INTO events VALUES(?,?,?,?,?)", 1, runA, "checkpoint", "2026-09-30T00:00:00.000Z", encode(map[string]any{"summary": "Durable checkpoint"}))
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, document
}
func openFixture(t *testing.T, path string, options ...Options) *Service {
	t.Helper()
	s, err := Open(path, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func call(t *testing.T, s *Service, args map[string]any) map[string]any {
	t.Helper()
	result, err := s.Call(context.Background(), wire.Scope{PrincipalID: "owner"}, "work_status", args)
	if err != nil {
		t.Fatal(err)
	}
	return result.StructuredContent
}
func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(body)
}
func TestArchiveContractPagesDoNotMutateOrClaim(t *testing.T) {
	path, _ := fixture(t)
	before := fileHash(t, path)
	s := openFixture(t, path)
	value := call(t, s, map[string]any{"action": "list"})
	if num(value["total_runs"]) != 2 {
		t.Fatalf("owner filter lost: %#v", value)
	}
	value = call(t, s, map[string]any{"action": "list", "claimed": true})
	if num(value["total_runs"]) != 1 {
		t.Fatalf("historical claimed filter: %#v", value)
	}
	value = call(t, s, map[string]any{"action": "get", "run_id": runA, "section": "summary"})
	if value["state"] != "ready" || value["claimed"] != true || value["inflight"] != false || num(value["unresolved_operations"]) != 1 {
		t.Fatalf("archive state changed: %#v", value)
	}
	value = call(t, s, map[string]any{"action": "get", "run_id": runA, "section": "iterations"})
	encoded, _ := json.Marshal(value)
	if strings.Contains(string(encoded), "private-session-hash") || strings.Contains(string(encoded), "private-clock") || strings.Contains(string(encoded), "principal_id") {
		t.Fatal("public iteration leaked credentials")
	}
	value = call(t, s, map[string]any{"action": "get", "run_id": runA, "section": "operations"})
	encoded, _ = json.Marshal(value)
	if strings.Contains(string(encoded), "private-operation-fingerprint") || strings.Contains(string(encoded), "receipt content") {
		t.Fatal("operation page leaked full receipt")
	}
	value = call(t, s, map[string]any{"action": "operation", "run_id": runA, "operation_id": "op1"})
	if str(obj(obj(value["receipt"])["result"])["stdout"]) != "receipt content" {
		t.Fatal("individual operation lost receipt")
	}
	if _, exists := obj(value["receipt"])["fingerprint"]; exists {
		t.Fatal("operation fingerprint exposed")
	}
	for _, section := range []string{"packet", "todos", "acceptance", "documents", "jobs", "activity", "source"} {
		call(t, s, map[string]any{"action": "get", "run_id": runA, "section": section})
	}
	value = call(t, s, map[string]any{"action": "history", "run_id": runA})
	if len(list(value["events"])) != 1 {
		t.Fatal("missing historical event")
	}
	value = call(t, s, map[string]any{"action": "search_memory", "project_id": "alpha", "query": "needle"})
	if num(value["total_matches"]) != 1 {
		t.Fatalf("memory search %#v", value)
	}
	if fileHash(t, path) != before {
		t.Fatal("read-only calls changed archive")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive reads created sidecars: %+v", entries)
	}
}
func TestOwnerReferencesAndExplicitPrincipalMapping(t *testing.T) {
	path, _ := fixture(t)
	s := openFixture(t, path)
	ctx := context.Background()
	if _, err := s.Call(ctx, wire.Scope{PrincipalID: "owner"}, "work_status", map[string]any{"action": "get", "run_id": runOther}); err == nil {
		t.Fatal("cross-principal run exposed")
	}
	if _, err := s.Call(ctx, wire.Scope{PrincipalID: "owner"}, "work_status", map[string]any{"action": "get", "run_id": "run_12345678"}); err == nil {
		t.Fatal("ambiguous prefix accepted")
	}
	value := call(t, s, map[string]any{"action": "get", "run_id": "run_12345678", "project_id": "alpha", "section": "summary"})
	if value["run_id"] != runA {
		t.Fatal("project prefix resolution failed")
	}
	value = call(t, s, map[string]any{"action": "get", "run_id": runA, "project_id": "wrong", "section": "summary"})
	if value["run_id"] != runA {
		t.Fatal("exact ID lost release precedence")
	}
	mapped := openFixture(t, path, Options{PrincipalMap: map[string]string{"new-owner": "owner"}})
	result, err := mapped.Call(ctx, wire.Scope{PrincipalID: "new-owner"}, "work_status", map[string]any{"action": "list"})
	if err != nil || num(result.StructuredContent["total_runs"]) != 2 {
		t.Fatal("explicit archive ownership mapping failed", err)
	}
}
func TestDocumentByteCursorsPreserveUTF8AndBriefingBudget(t *testing.T) {
	path, want := fixture(t)
	s := openFixture(t, path, Options{PacketBytes: 4096})
	offset := 0
	var rebuilt strings.Builder
	for attempt := 0; attempt < 100; attempt++ {
		value := call(t, s, map[string]any{"action": "read_document", "run_id": runA, "document_id": "doc_spec", "offset": offset, "max_bytes": 512})
		chunk := str(value["content"])
		if !utf8.ValidString(chunk) || num(value["offset"]) != int64(offset) {
			t.Fatalf("invalid byte cursor %#v", value)
		}
		rebuilt.WriteString(chunk)
		if value["next_offset"] == nil {
			break
		}
		next := int(num(value["next_offset"]))
		if next <= offset {
			t.Fatal("document cursor did not advance")
		}
		offset = next
	}
	if rebuilt.String() != want {
		t.Fatalf("document lost bytes: %d vs %d", rebuilt.Len(), len(want))
	}
	result, err := s.Call(context.Background(), wire.Scope{PrincipalID: "owner"}, "work_status", map[string]any{"action": "get", "run_id": runA})
	if err != nil {
		t.Fatal(err)
	}
	if jsonBytes(result) > 4096 {
		t.Fatalf("briefing exceeds contract budget: %d", jsonBytes(result))
	}
	if result.StructuredContent["handling"] == nil || obj(result.StructuredContent["packet"])["documents"] == nil {
		t.Fatal("briefing lost full-document navigation")
	}
}
func TestImmutableArchiveConcurrentReadsAndPortableExport(t *testing.T) {
	path, _ := fixture(t)
	before := fileHash(t, path)
	s := openFixture(t, path, Options{Clock: func() time.Time { return time.Date(2026, 10, 31, 17, 0, 0, 0, time.UTC) }})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 5; k++ {
				if _, err := s.Call(context.Background(), wire.Scope{PrincipalID: "owner"}, "work_status", map[string]any{"action": "get", "section": "summary", "run_id": runA}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	var output bytes.Buffer
	receipt, err := s.Export(context.Background(), wire.Scope{PrincipalID: "owner"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Runs != 2 || receipt.Bytes != int64(output.Len()) || receipt.Records < 8 {
		t.Fatalf("incomplete archive export %+v", receipt)
	}
	if strings.Contains(output.String(), "private-session-hash") || strings.Contains(output.String(), "private-operation-fingerprint") || strings.Contains(output.String(), runOther) {
		t.Fatal("archive export crossed privacy boundary")
	}
	if fileHash(t, path) != before {
		t.Fatal("concurrent reads/export mutated archive")
	}
}
func TestRejectIncompleteWALCopyAndEmptyArchive(t *testing.T) {
	path, _ := fixture(t)
	if err := os.WriteFile(path+"-wal", []byte("uncheckpointed"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("ignored uncheckpointed WAL")
	}
	s := openFixture(t, "")
	value := call(t, s, map[string]any{"action": "list"})
	if num(value["total_runs"]) != 0 || len(list(value["runs"])) != 0 {
		t.Fatal("empty archive contract")
	}
	if _, err := s.Call(context.Background(), wire.Scope{}, "work_status", map[string]any{}); err == nil {
		t.Fatal("unauthenticated work context allowed")
	}
}
