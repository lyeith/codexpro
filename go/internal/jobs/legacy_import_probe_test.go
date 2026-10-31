package jobs

// This opt-in proof consumes an actual private TypeScript snapshot and a JSON
// oracle produced by the exact release's exported JobOutputStore. Ordinary unit
// tests skip it. It never calls start/stop/Bash or points a manager at TS state.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type legacyOracleJob struct {
	ID          string         `json:"id"`
	WorkspaceID string         `json:"workspace_id"`
	Disposition string         `json:"disposition"`
	Metadata    map[string]any `json:"metadata"`
	View        map[string]any `json:"view"`
	Head        map[string]any `json:"head"`
	Tail        map[string]any `json:"tail"`
	Pages       []struct {
		Cursor string         `json:"cursor"`
		Budget int            `json:"budget"`
		Result map[string]any `json:"result"`
	} `json:"pages"`
	PageError *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"page_error"`
}
type legacyOracle struct {
	SchemaVersion           int                `json:"schema_version"`
	SourceCommit            string             `json:"source_commit"`
	SnapshotDir             string             `json:"snapshot_dir"`
	OriginalJobsDir         string             `json:"original_jobs_dir"`
	MetadataSHA256          string             `json:"metadata_sha256"`
	ReferenceModule         string             `json:"reference_module"`
	ReferenceModuleSHA256   string             `json:"reference_module_sha256"`
	RetentionMS             int64              `json:"retention_ms"`
	SourceProcessOperations int                `json:"source_process_operations"`
	Files                   []LegacyImportFile `json:"files"`
	Jobs                    []legacyOracleJob  `json:"jobs"`
}

type legacyProofResolver map[string]wire.Workspace

func (r legacyProofResolver) Resolve(_ context.Context, _ wire.Scope, id string) (wire.Workspace, error) {
	w, ok := r[id]
	if !ok {
		return w, wire.Errorf("workspace_not_found", "Unknown workspace_id: %s", id)
	}
	return w, nil
}
func (r legacyProofResolver) Projects() []wire.Project {
	seen := map[string]bool{}
	result := []wire.Project{}
	for _, w := range r {
		if !seen[w.ProjectID] {
			seen[w.ProjectID] = true
			result = append(result, wire.Project{ID: w.ProjectID, Root: w.Root, Label: w.ProjectID})
		}
	}
	return result
}
func (r legacyProofResolver) Project(id string) (wire.Project, bool) {
	for _, p := range r.Projects() {
		if p.ID == id {
			return p, true
		}
	}
	return wire.Project{}, false
}

func normalizeLegacyProof(value any) any {
	b, _ := json.Marshal(value)
	var normalized any
	_ = json.Unmarshal(b, &normalized)
	return normalized
}
func legacyOutputMap(out Output) map[string]any {
	return map[string]any{"stdout": out.Stdout, "stderr": out.Stderr, "stdout_bytes": out.StdoutBytes, "stderr_bytes": out.StderrBytes, "truncated": out.Truncated}
}

func TestLegacyJobMigrationSnapshotProof(t *testing.T) {
	oraclePath := os.Getenv("SP6_LEGACY_JOBS_ORACLE")
	if oraclePath == "" {
		t.Skip("an actual private TS job snapshot and exact-release oracle were not supplied")
	}
	state := os.Getenv("SP6_LEGACY_JOBS_STATE")
	resultPath := os.Getenv("SP6_LEGACY_JOBS_RESULT")
	expectedOracle := os.Getenv("SP6_LEGACY_JOBS_ORACLE_SHA256")
	if !filepath.IsAbs(oraclePath) || !filepath.IsAbs(state) || !filepath.IsAbs(resultPath) || !legacySHA256.MatchString(expectedOracle) {
		t.Fatal("absolute private paths and expected oracle SHA-256 are required")
	}
	for _, dir := range []string{filepath.Dir(oraclePath), filepath.Dir(resultPath)} {
		if err := config.New(dir).ValidateIsolation(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatal("projection proof requires a never-used destination state")
	}
	if _, err := os.Lstat(resultPath); !os.IsNotExist(err) {
		t.Fatal("proof result already exists")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var oracleBytes bytes.Buffer
	oracleFile, err := readLegacyFile(ctx, filepath.Dir(oraclePath), filepath.Base(oraclePath), legacyImportMetadataLimit, &oracleBytes)
	if err != nil || oracleFile.SHA256 != expectedOracle {
		t.Fatalf("oracle digest/read: %s %v", oracleFile.SHA256, err)
	}
	var oracle legacyOracle
	if err = json.Unmarshal(oracleBytes.Bytes(), &oracle); err != nil {
		t.Fatal(err)
	}
	if oracle.SchemaVersion != 1 || oracle.SourceCommit != "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a" || oracle.SourceProcessOperations != 0 || !legacySHA256.MatchString(oracle.ReferenceModuleSHA256) || oracle.RetentionMS < 1 || oracle.RetentionMS > 31536000000 {
		t.Fatal("oracle is not an exact-release private read-only reference")
	}
	eligibleCount := 0
	renderedBytes := int64(0)
	for _, job := range oracle.Jobs {
		if job.Disposition == "imported_history" {
			eligibleCount++
		}
	}
	for _, file := range oracle.Files {
		if filepath.Ext(file.Path) == ".log" {
			renderedBytes += file.Bytes
		}
	}
	if eligibleCount == 0 || renderedBytes == 0 {
		t.Fatal("actual migration proof needs finished private TS history with nonempty retained output")
	}
	if process.Within(oracle.SnapshotDir, resultPath) || process.Within(state, resultPath) {
		t.Fatal("proof result must be outside the source snapshot and destination stores")
	}
	t.Setenv("CODEXPRO_JOB_SCOPES", "0")
	cfg := config.New(state)
	cfg.Set("jobRetentionMs", oracle.RetentionMS)
	cfg.Set("maxRetainedJobBytes", int64(2<<30))
	cfg.Set("maxJobHistoryPerWorkspace", 10000)
	options := LegacyImportOptions{SourceDir: oracle.SnapshotDir, OriginalJobsDir: oracle.OriginalJobsDir, ExpectedMetadataSHA256: oracle.MetadataSHA256}
	report, err := ImportLegacySnapshot(ctx, cfg, options)
	if err != nil {
		t.Fatal(err)
	}
	var archived bytes.Buffer
	if _, err = readLegacyFile(ctx, state, "legacy-jobs/jobs.json", legacyImportMetadataLimit, &archived); err != nil {
		t.Fatal(err)
	}
	var table struct {
		Jobs []Record `json:"jobs"`
	}
	if err = json.Unmarshal(archived.Bytes(), &table); err != nil {
		t.Fatal(err)
	}
	resolver := legacyProofResolver{}
	for _, r := range table.Jobs {
		w := wire.Workspace{ID: r.WorkspaceID, ProjectID: r.ProjectID, Root: r.Root, Mode: "workspace"}
		if old, ok := resolver[w.ID]; ok && old.Root != w.Root {
			t.Fatal("one historical workspace identity names different roots")
		}
		resolver[w.ID] = w
	}
	differences := []map[string]any{}
	checks := 0
	compare := func(label string, want, got any) {
		checks++
		if !reflect.DeepEqual(normalizeLegacyProof(want), normalizeLegacyProof(got)) {
			if len(differences) < 100 {
				differences = append(differences, map[string]any{"check": label, "expected": want, "actual": got})
			}
		}
	}
	files := map[string]LegacyImportFile{}
	for _, f := range report.Files {
		files[f.Path] = f
	}
	for _, expected := range oracle.Files {
		compare("import manifest "+expected.Path, expected, files[expected.Path])
	}
	compare("complete copied file census", len(oracle.Files)+1, len(report.Files))
	compare("complete source record census", len(oracle.Jobs), report.SourceRecords)
	for pass := 0; pass < 2; pass++ {
		s, openErr := New(cfg, resolver)
		if openErr != nil {
			t.Fatal(openErr)
		}
		for _, expected := range oracle.Jobs {
			label := fmt.Sprintf("pass%d/%s", pass, expected.ID)
			r, ok := s.Get(expected.ID, expected.WorkspaceID)
			compare(label+" disposition", expected.Disposition == "imported_history", ok)
			if expected.Disposition != "imported_history" || !ok {
				continue
			}
			compare(label+" no inherited process authority", true, r.PID == 0 && r.ProcessIdentity == "" && r.ScopeUnit == "" && r.ScopeIdentity == nil && r.ResourceClaim == nil && r.ControlPath == "" && r.GrantPath == "" && r.SpecPath == "" && r.ExitPath == "" && r.ResultPath == "" && r.LaunchState == "historical" && r.Quiescent && r.Status != "running")
			compare(label+" metadata", expected.Metadata, s.OutputMetadata(r))
			wantedView := map[string]any{}
			for k, v := range expected.Metadata {
				wantedView[k] = v
			}
			for k, v := range expected.View {
				wantedView[k] = v
			}
			compare(label+" exact source view", wantedView, s.jobView(r, nil, false))
			for _, mode := range []string{"head", "tail"} {
				out, readErr := s.read(r, 256, mode == "head")
				if readErr != nil {
					differences = append(differences, map[string]any{"check": label + " " + mode, "error": readErr.Error()})
					continue
				}
				want := expected.Tail
				if mode == "head" {
					want = expected.Head
				}
				compare(label+" "+mode, want, legacyOutputMap(out))
			}
			if expected.PageError != nil {
				_, pageErr := s.page(r, "", 256)
				var business *wire.Error
				if !errors.As(pageErr, &business) {
					differences = append(differences, map[string]any{"check": label + " expired page", "error": fmt.Sprint(pageErr)})
				} else {
					compare(label+" expired code", expected.PageError.Code, business.Code)
					compare(label+" expired message", expected.PageError.Message, business.Message)
				}
			}
			for i, page := range expected.Pages {
				actual, pageErr := s.page(r, page.Cursor, page.Budget)
				if pageErr != nil {
					differences = append(differences, map[string]any{"check": label + " cursor", "error": pageErr.Error()})
					continue
				}
				compare(fmt.Sprintf("%s source cursor %d", label, i), page.Result, actual.fields())
				result, callErr := s.Call(ctx, wire.Scope{}, "jobs", map[string]any{"workspace_id": r.WorkspaceID, "job_ids": []string{r.ID}, "output": "incremental", "cursor": page.Cursor, "max_bytes": page.Budget, "wait_ms": 0})
				if callErr != nil {
					differences = append(differences, map[string]any{"check": label + " public cursor", "error": callErr.Error()})
					continue
				}
				data := normalizeLegacyProof(result.StructuredContent).(map[string]any)
				views, ok := data["jobs"].([]any)
				if !ok || len(views) != 1 {
					t.Fatalf("invalid public jobs result: %#v", data)
				}
				view := views[0].(map[string]any)
				selected := map[string]any{}
				for k := range page.Result {
					selected[k] = view[k]
				}
				compare(fmt.Sprintf("%s public source cursor %d", label, i), page.Result, selected)
			}
			after, _ := s.Get(r.ID, r.WorkspaceID)
			compare(label+" reads preserve acknowledgement", r.Acknowledged, after.Acknowledged)
		}
		var completions, pins int
		if err = s.db.QueryRow("SELECT count(*) FROM job_completions").Scan(&completions); err != nil {
			t.Fatal(err)
		}
		if err = s.db.QueryRow("SELECT count(*) FROM job_inputs").Scan(&pins); err != nil {
			t.Fatal(err)
		}
		compare(fmt.Sprintf("pass%d no re-emitted completion", pass), 0, completions)
		compare(fmt.Sprintf("pass%d no adopted active pin", pass), 0, pins)
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		for _, expected := range oracle.Files {
			actual, readErr := readLegacyFile(ctx, filepath.Join(state, "jobs"), expected.Path, 256<<20, io.Discard)
			if readErr != nil {
				differences = append(differences, map[string]any{"check": expected.Path + " retained bytes", "error": readErr.Error()})
				continue
			}
			compare(fmt.Sprintf("pass%d complete retained bytes %s", pass, expected.Path), expected, actual)
		}
	}
	replay, err := ImportLegacySnapshot(ctx, cfg, options)
	if err != nil {
		t.Fatal(err)
	}
	compare("matching import is a receipt replay", true, replay.Replayed)
	compare("repeat import preserves receipt identity", report.ImportedAt, replay.ImportedAt)
	proof := map[string]any{"schema_version": 1, "source_commit": oracle.SourceCommit, "reference_module": oracle.ReferenceModule, "reference_module_sha256": oracle.ReferenceModuleSHA256, "oracle_sha256": oracleFile.SHA256, "import": report, "retention_ms": oracle.RetentionMS, "output_byte_checks": "every imported raw/rendered file, before and after reopen", "projection_passes": 2, "source_process_operations": 0, "checks": checks, "differences": differences, "passed": len(differences) == 0}
	b, err := json.MarshalIndent(proof, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = process.ExclusiveWrite(resultPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if len(differences) > 0 {
		t.Fatalf("legacy job migration projection had %d differences; evidence %s", len(differences), resultPath)
	}
	t.Logf("actual private TypeScript job migration: %d checks, %d imported, %d inert archive-only; evidence %s", checks, report.ImportedRecords, report.ArchiveOnlyRecords, resultPath)
}
