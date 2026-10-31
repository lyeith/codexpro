package workhistory

// This opt-in proof exercises the real public Export method against the exact
// captured release archive. It opens only the disposable normalized copy made
// by historical_work_contracts.py; the published capture is hashed as a file.
// Historical content stays in private JSONL files. The summary contains only
// counts, sizes, digests and invariants suitable for repository evidence.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const workExportCaptureSHA = "50d09a5cc88c068f897e8734676d784488c9828d65b4da2ae7024f48483f0adf"
const workExportMaximumBytes int64 = 1024 * 1024 * 1024
const workExportMaximumRecordBytes = 32 * 1024 * 1024

var workExportCaptureCounts = map[string]int64{
	"documents": 1516, "events": 19406, "iterations": 231, "metadata": 3,
	"operations": 17705, "requests": 1369, "runs": 9, "sessions": 173,
	"sqlite_sequence": 1, "work_jobs": 11934,
}

type workExportPlan struct {
	contractPlan
	PreservedTableCounts map[string]int64 `json:"preserved_table_counts"`
}

type workExportSourceRun struct {
	id        string
	principal string
	body      map[string]any
}

type workExportBudgetWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *workExportBudgetWriter) Write(value []byte) (int, error) {
	if int64(len(value)) > w.remaining {
		return 0, errors.New("historical export exceeds the remaining 1 GiB evidence budget")
	}
	n, err := w.writer.Write(value)
	w.remaining -= int64(n)
	return n, err
}

func workExportCanonical(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func workExportNoSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			return errors.New("normalized archive has an unexpected sidecar")
		}
	}
	return nil
}

func workExportJSON(body string) (map[string]any, error) {
	if len(body) > workExportMaximumRecordBytes {
		return nil, errors.New("archived JSON record exceeds the 32 MiB bound")
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		return nil, errors.New("archived record is not valid JSON")
	}
	if value == nil {
		return nil, errors.New("archived record is not a JSON object")
	}
	return value, nil
}

type workExportVerifier struct {
	scanner             *bufio.Scanner
	records             int
	counts              map[string]int64
	logicalHash         hash.Hash
	documentRowsHash    hash.Hash
	documentOutputHash  hash.Hash
	documentKeys        map[string]bool
	documentIDs         map[string]bool
	sourceDocumentBytes int64
	outputDocumentBytes int64
	redactedDocuments   int64
}

func workExportNewVerifier(reader io.Reader) *workExportVerifier {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 8192), workExportMaximumRecordBytes+1)
	return &workExportVerifier{
		scanner: scanner, counts: map[string]int64{}, logicalHash: sha256.New(),
		documentRowsHash: sha256.New(), documentOutputHash: sha256.New(),
		documentKeys: map[string]bool{}, documentIDs: map[string]bool{},
	}
}

// expect compares every exported key/value with the immutable source row after
// the documented credential projection. No exporter/store helpers are reused:
// only the separately tested shared secret redactor defines public redaction.
func (v *workExportVerifier) expect(kind string, source map[string]any) error {
	if !v.scanner.Scan() {
		if err := v.scanner.Err(); err != nil {
			return err
		}
		return errors.New("export ended before all source rows were verified")
	}
	var actual map[string]any
	if err := json.Unmarshal(v.scanner.Bytes(), &actual); err != nil || actual == nil {
		return errors.New("export contains an invalid JSON object")
	}
	expected := map[string]any{"kind": kind, "record": wire.RedactValue(source)}
	expectedBytes, err := workExportCanonical(expected)
	if err != nil {
		return err
	}
	actualBytes, err := workExportCanonical(actual)
	if err != nil {
		return err
	}
	if !bytes.Equal(expectedBytes, actualBytes) {
		// Keep historical payloads, identifiers and credentials out of logs.
		expectedHash, actualHash := sha256.Sum256(expectedBytes), sha256.Sum256(actualBytes)
		return fmt.Errorf("export projection mismatch at record %d kind %s; expected_sha256=%x actual_sha256=%x", v.records+1, kind, expectedHash, actualHash)
	}
	v.records++
	v.counts[kind]++
	_, _ = v.logicalHash.Write(actualBytes)
	_, _ = v.logicalHash.Write([]byte{'\n'})
	if kind == "documents" {
		id, idOK := source["id"].(string)
		runID, runOK := source["run_id"].(string)
		revision, revisionOK := source["revision"].(float64)
		content, contentOK := source["content"].(string)
		if !idOK || !runOK || !revisionOK || revision < 1 || revision != float64(int64(revision)) || !contentOK {
			return errors.New("document revision lacks its complete source identity/content")
		}
		key := fmt.Sprintf("%s\x00%s\x00%d", runID, id, int64(revision))
		if v.documentKeys[key] {
			return errors.New("export repeats a document revision")
		}
		v.documentKeys[key] = true
		v.documentIDs[id] = true
		sourceBytes, err := workExportCanonical(source)
		if err != nil {
			return err
		}
		_, _ = v.documentRowsHash.Write(sourceBytes)
		_, _ = v.documentRowsHash.Write([]byte{'\n'})
		_, _ = v.documentOutputHash.Write(actualBytes)
		_, _ = v.documentOutputHash.Write([]byte{'\n'})
		publicRecord, ok := actual["record"].(map[string]any)
		if !ok {
			return errors.New("exported document is not an object")
		}
		publicContent, ok := publicRecord["content"].(string)
		if !ok {
			return errors.New("exported document lost its content string")
		}
		v.sourceDocumentBytes += int64(len(content))
		v.outputDocumentBytes += int64(len(publicContent))
		if publicContent != content {
			v.redactedDocuments++
		}
	}
	return nil
}

func workExportReadRuns(ctx context.Context, db *sql.DB) ([]workExportSourceRun, error) {
	rows, err := db.QueryContext(ctx, "SELECT id,principal,body FROM runs ORDER BY rowid DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []workExportSourceRun{}
	seen := map[string]bool{}
	for rows.Next() {
		var id, principal, body string
		if err := rows.Scan(&id, &principal, &body); err != nil {
			return nil, err
		}
		value, err := workExportJSON(body)
		if err != nil {
			return nil, err
		}
		if id == "" || principal == "" || seen[id] || value["id"] != id || value["principal_id"] != principal {
			return nil, errors.New("archived run row identity is inconsistent")
		}
		seen[id] = true
		runs = append(runs, workExportSourceRun{id: id, principal: principal, body: value})
	}
	return runs, rows.Err()
}

func workExportVerifyRun(ctx context.Context, db *sql.DB, v *workExportVerifier, run workExportSourceRun) error {
	publicRun := map[string]any{}
	for key, value := range run.body {
		if key != "principal_id" {
			publicRun[key] = value
		}
	}
	if err := v.expect("run", publicRun); err != nil {
		return err
	}
	for _, table := range []string{"iterations", "operations", "documents", "work_jobs"} {
		query := "SELECT body FROM " + table + " WHERE run_id=? ORDER BY rowid"
		if table == "documents" {
			query = "SELECT id,revision,body FROM documents WHERE run_id=? ORDER BY id,revision"
		}
		rows, err := db.QueryContext(ctx, query, run.id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var body, documentID string
			var revision int64
			if table == "documents" {
				err = rows.Scan(&documentID, &revision, &body)
			} else {
				err = rows.Scan(&body)
			}
			if err != nil {
				rows.Close()
				return err
			}
			value, err := workExportJSON(body)
			if err != nil {
				rows.Close()
				return err
			}
			if table == "documents" && (value["id"] != documentID || value["revision"] != float64(revision) || value["run_id"] != run.id) {
				rows.Close()
				return errors.New("stored document revision identity differs from its physical SQL row")
			}
			if table == "iterations" {
				delete(value, "token_hash")
				delete(value, "clock")
				delete(value, "principal_id")
			}
			if table == "operations" {
				delete(value, "fingerprint")
			}
			if err = v.expect(table, value); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	rows, err := db.QueryContext(ctx, "SELECT sequence,kind,at,body FROM events WHERE run_id=? ORDER BY sequence", run.id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int64
		var kind, at, body string
		if err = rows.Scan(&sequence, &kind, &at, &body); err != nil {
			return err
		}
		payload, err := workExportJSON(body)
		if err != nil {
			return err
		}
		if err = v.expect("event", map[string]any{"sequence": sequence, "run_id": run.id, "kind": kind, "at": at, "body": payload}); err != nil {
			return err
		}
	}
	return rows.Err()
}

// TestHistoricalFullExportProbe requires the captured SHA and exact table
// counts: an empty fixture cannot prove preservation of the nine original runs.
func TestHistoricalFullExportProbe(t *testing.T) {
	planName := os.Getenv("SP6_WORK_EXPORT_PLAN")
	if planName == "" {
		t.Skip("explicit historical full-export plan not supplied")
	}
	destinationName := os.Getenv("SP6_WORK_EXPORT_DEST")
	if destinationName == "" {
		t.Fatal("SP6_WORK_EXPORT_DEST is required")
	}
	planPath, err := contractPrivatePath(planName)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(planPath)
	planInfo, err := os.Stat(planPath)
	if err != nil || !planInfo.Mode().IsRegular() || planInfo.Size() > 1024*1024 {
		t.Fatal("export plan must be a bounded regular file")
	}
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	var plan workExportPlan
	if err = json.Unmarshal(planBytes, &plan); err != nil {
		t.Fatal("invalid historical export plan JSON")
	}
	if plan.SchemaVersion != 1 || plan.SourceCommit != "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a" || !plan.DisposableCopy || plan.Archive.SHA256 != workExportCaptureSHA || !reflect.DeepEqual(plan.PreservedTableCounts, workExportCaptureCounts) {
		t.Fatal("plan does not identify the complete approved nine-run capture")
	}
	clock, err := time.Parse(time.RFC3339Nano, plan.Clock)
	if err != nil || clock.UTC().Format("2006-01-02T15:04:05.000Z") != plan.Clock {
		t.Fatal("invalid fixed export clock")
	}
	archive, err := contractPrivatePath(plan.Archive.Path)
	if err != nil || !contractWithin(root, archive) {
		t.Fatal("export source escapes its disposable fixture")
	}
	archiveInfo, err := os.Lstat(plan.Archive.Path)
	if err != nil || !archiveInfo.Mode().IsRegular() || archiveInfo.Mode()&os.ModeSymlink != 0 || archiveInfo.Mode().Perm()&0222 != 0 {
		t.Fatal("export requires the separate read-only regular consumer archive")
	}
	if info, ok := archiveInfo.Sys().(*syscall.Stat_t); !ok || info.Nlink != 1 {
		t.Fatal("consumer archive must not have another hard link")
	}
	published, err := contractPrivatePath(plan.SourceArchive)
	if err != nil || published == archive || contractWithin(root, published) {
		t.Fatal("published archive must remain separate from the disposable fixture")
	}
	publishedInfo, err := os.Stat(published)
	if err != nil || os.SameFile(archiveInfo, publishedInfo) {
		t.Fatal("consumer is not an independent copy of the published archive")
	}
	for _, path := range []string{archive, published} {
		if err = workExportNoSidecars(path); err != nil {
			t.Fatal(err)
		}
		digest, count, err := contractDigest(path)
		if err != nil || digest != workExportCaptureSHA || count != plan.Archive.Bytes {
			t.Fatal("published/consumer archive differs from the captured bytes")
		}
	}
	destination, err := filepath.Abs(destinationName)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := contractPrivatePath(filepath.Dir(destination))
	if err != nil || parent != root || filepath.Dir(destination) != root || destination == filepath.Dir(archive) {
		t.Fatal("export destination must be a new direct child of the private plan directory")
	}
	if _, err = os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatal("export destination already exists")
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	u := url.URL{Scheme: "file", Path: archive}
	checker, err := sql.Open("sqlite3", u.String()+"?mode=ro&immutable=1&_query_only=on&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer checker.Close()
	checker.SetMaxOpenConns(1)
	var queryOnly int
	if err = checker.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
		t.Fatal("export verifier must use a query-only immutable connection")
	}
	for table, expected := range workExportCaptureCounts {
		var count int64
		if err = checker.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != expected {
			t.Fatalf("captured table count mismatch for %s", table)
		}
	}
	runs, err := workExportReadRuns(ctx, checker)
	if err != nil || len(runs) != 9 {
		t.Fatal("complete archived run identities could not be read")
	}
	principalSet := map[string]bool{}
	states := map[string]int{}
	for _, run := range runs {
		principalSet[run.principal] = true
		state, ok := run.body["state"].(string)
		if !ok {
			t.Fatal("archived run lacks a state")
		}
		states[state]++
	}
	principals := make([]string, 0, len(principalSet))
	for principal := range principalSet {
		principals = append(principals, principal)
	}
	sort.Strings(principals)
	history, err := Open(archive, Options{Clock: func() time.Time { return clock }, PacketBytes: plan.PacketBytes, ContextDir: plan.ContextDir})
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	var denied bytes.Buffer
	if _, err = history.Export(ctx, wire.Scope{}, &denied); err == nil || denied.Len() != 0 {
		t.Fatal("unauthenticated export did not fail without writing historical content")
	}
	unownedPrincipal := "sp6_export_unowned_" + workExportCaptureSHA
	if principalSet[unownedPrincipal] {
		t.Fatal("negative export principal unexpectedly exists in the archive")
	}
	var unowned bytes.Buffer
	unownedSummary, err := history.Export(ctx, wire.Scope{PrincipalID: unownedPrincipal}, &unowned)
	if err != nil || unownedSummary.Runs != 0 || unownedSummary.Records != 1 {
		t.Fatal("unowned export exposed historical rows")
	}
	header := map[string]any{"schema_version": "1.0", "exported_at": plan.Clock, "read_only": true, "source": "release-0.31 work history", "current_source_observed": false}
	negativeVerifier := workExportNewVerifier(bytes.NewReader(unowned.Bytes()))
	if err = negativeVerifier.expect("archive", header); err != nil || negativeVerifier.scanner.Scan() || negativeVerifier.scanner.Err() != nil {
		t.Fatal("unowned export differs from the header-only public contract")
	}
	outputs := []map[string]any{}
	allCounts := map[string]int64{}
	allDocuments, allDocumentIDs := map[string]bool{}, map[string]bool{}
	var totalBytes, sourceDocumentBytes, outputDocumentBytes, redactedDocuments int64
	for index, principal := range principals {
		filename := fmt.Sprintf("principal-%02d.jsonl", index+1)
		path := filepath.Join(destination, filename)
		output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		bounded := &workExportBudgetWriter{writer: output, remaining: workExportMaximumBytes - totalBytes}
		summary, exportErr := history.Export(ctx, wire.Scope{PrincipalID: principal}, bounded)
		syncErr := output.Sync()
		closeErr := output.Close()
		if exportErr != nil || syncErr != nil || closeErr != nil {
			t.Fatalf("export %d failed: export=%v sync=%v close=%v", index+1, exportErr, syncErr, closeErr)
		}
		digest, count, err := contractDigest(path)
		if err != nil || digest != summary.SHA256 || count != summary.Bytes || count <= 0 {
			t.Fatal("ExportSummary does not match the durable output bytes/digest")
		}
		totalBytes += count
		input, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		verifier := workExportNewVerifier(input)
		if err = verifier.expect("archive", header); err != nil {
			input.Close()
			t.Fatal(err)
		}
		ownedRuns := 0
		for _, run := range runs {
			if run.principal != principal {
				continue
			}
			ownedRuns++
			if err = workExportVerifyRun(ctx, checker, verifier, run); err != nil {
				input.Close()
				t.Fatal(err)
			}
		}
		if verifier.scanner.Scan() || verifier.scanner.Err() != nil {
			input.Close()
			t.Fatal("export contains extra rows or an unreadable trailing record")
		}
		if err = input.Close(); err != nil {
			t.Fatal(err)
		}
		if summary.Runs != ownedRuns || summary.Records != verifier.records {
			t.Fatal("ExportSummary row/run totals differ from the verified record stream")
		}
		for kind, count := range verifier.counts {
			allCounts[kind] += count
		}
		for key := range verifier.documentKeys {
			if allDocuments[key] {
				t.Fatal("a document revision was exported under more than one principal")
			}
			allDocuments[key] = true
		}
		for id := range verifier.documentIDs {
			allDocumentIDs[id] = true
		}
		sourceDocumentBytes += verifier.sourceDocumentBytes
		outputDocumentBytes += verifier.outputDocumentBytes
		redactedDocuments += verifier.redactedDocuments
		outputs = append(outputs, map[string]any{
			"file": filename, "summary": summary, "records_by_kind": verifier.counts,
			"all_row_projections_match": true, "logical_records_sha256": hex.EncodeToString(verifier.logicalHash.Sum(nil)),
			"source_document_rows_sha256":    hex.EncodeToString(verifier.documentRowsHash.Sum(nil)),
			"public_document_records_sha256": hex.EncodeToString(verifier.documentOutputHash.Sum(nil)),
			"source_document_content_bytes":  verifier.sourceDocumentBytes, "public_document_content_bytes": verifier.outputDocumentBytes,
			"documents_changed_by_public_redaction": verifier.redactedDocuments,
		})
	}
	expectedCounts := map[string]int64{"archive": int64(len(principals)), "run": 9, "iterations": 231, "operations": 17705, "documents": 1516, "work_jobs": 11934, "event": 19406}
	if !reflect.DeepEqual(allCounts, expectedCounts) || len(allDocuments) != 1516 || len(allDocumentIDs) != 465 {
		t.Fatal("full export did not cover every captured run and document revision")
	}
	if err = history.Close(); err != nil {
		t.Fatal(err)
	}
	if err = checker.Close(); err != nil {
		t.Fatal(err)
	}
	for _, checkpoint := range []struct {
		path    string
		initial os.FileInfo
	}{{archive, archiveInfo}, {published, publishedInfo}} {
		if err = workExportNoSidecars(checkpoint.path); err != nil {
			t.Fatal(err)
		}
		digest, count, err := contractDigest(checkpoint.path)
		after, statErr := os.Stat(checkpoint.path)
		if err != nil || statErr != nil || digest != workExportCaptureSHA || count != plan.Archive.Bytes || !os.SameFile(checkpoint.initial, after) || !checkpoint.initial.ModTime().Equal(after.ModTime()) {
			t.Fatal("full export changed or replaced a published/consumer archive")
		}
	}
	planAfter, err := os.ReadFile(planPath)
	if err != nil || !bytes.Equal(planBytes, planAfter) {
		t.Fatal("export plan changed during execution")
	}
	proof := map[string]any{
		"schema_version": 1, "source_commit": plan.SourceCommit, "lane": "native-full-historical-export", "passed": true,
		"archive_sha256": workExportCaptureSHA, "archive_bytes": plan.Archive.Bytes, "fixed_clock": plan.Clock,
		"published_archive_unchanged": true, "disposable_archive_unchanged": true, "sidecars_created": false,
		"live_sqlite_connections": 0, "historical_runs_executed": 0, "principal_mapping": "none; original archived ownership",
		"principals": len(principals), "runs": 9, "run_states_preserved": states, "document_ids": 465, "document_revisions_verified": 1516,
		"records_by_kind": allCounts, "all_source_row_projections_match": true, "exported_bytes": totalBytes,
		"source_document_content_bytes": sourceDocumentBytes, "public_document_content_bytes": outputDocumentBytes,
		"documents_changed_by_public_redaction": redactedDocuments, "outputs": outputs,
		"complete_archive_table_counts":        workExportCaptureCounts,
		"private_archive_only_tables":          []string{"metadata", "requests", "sessions", "sqlite_sequence"},
		"unauthenticated_export_wrote_nothing": true, "foreign_principal_export_header_only": true,
		"scope": "Every public export record compared against independent read-only SQL rows with the documented credential projection and shared secret redactor. The full immutable SQLite archive separately preserves all table rows, including sessions and request receipts. No coordinator, sweeper, session issuer, process launcher, audit append or live SQLite connection.",
	}
	if err = contractWriteJSON(filepath.Join(destination, "summary.json"), proof); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err = directory.Sync(); err != nil {
		directory.Close()
		t.Fatal(err)
	}
	if err = directory.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("historical full export: runs=9 document_ids=465 revisions=1516 principals=%d bytes=%d all_rows_match=true both_archives_unchanged=true", len(principals), totalBytes)
}
