package legacyworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This gate is skipped unless the operator supplies an explicit independent
// snapshot made by capture_legacy_workspaces.py. A normal unit-test pass is not
// evidence that actual retained source state was imported.
func TestCapturedLegacyWorkspaceSnapshot(t *testing.T) {
	planPath := os.Getenv("CODEXPRO_LEGACY_WORKSPACE_PLAN")
	if planPath == "" {
		t.Skip("explicit retained lease/catalog snapshot not supplied")
	}
	planRaw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(planRaw) > 1_000_000 {
		t.Fatal("probe plan exceeds bound")
	}
	var plan struct {
		SchemaVersion int    `json:"schema_version"`
		SnapshotRoot  string `json:"snapshot_root"`
		CatalogFile   string `json:"catalog_file"`
		ManagedRoot   string `json:"managed_root"`
		Destination   string `json:"destination"`
		CaptureSHA    string `json:"capture_manifest_sha256"`
		Files         []File `json:"files"`
	}
	if err = json.Unmarshal(planRaw, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != 1 || len(plan.Files) < 1 {
		t.Fatal("unsupported or empty capture plan")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root, err := filepath.EvalSymlinks(plan.SnapshotRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = snapshotPath(root, planPath); err != nil {
		t.Fatal("probe plan is not inside its captured snapshot")
	}
	captureFile, err := readSnapshotFile(ctx, root, filepath.Join(root, "capture-manifest.json"), 1_000_000)
	if err != nil || captureFile.receipt.SHA256 != plan.CaptureSHA {
		t.Fatalf("capture provenance mismatch: %v", err)
	}
	var captureReceipt struct {
		SourceUnchanged bool `json:"source_unchanged_verified"`
		ManagerStarted  bool `json:"reference_manager_instantiated"`
		Files           []struct {
			SourcePath   string `json:"source_path"`
			SnapshotPath string `json:"snapshot_path"`
			SHA256       string `json:"sha256"`
			Bytes        int64  `json:"bytes"`
		} `json:"files"`
	}
	if err = json.Unmarshal(captureFile.bytes, &captureReceipt); err != nil {
		t.Fatal(err)
	}
	if !captureReceipt.SourceUnchanged || captureReceipt.ManagerStarted || len(captureReceipt.Files) != len(plan.Files) {
		t.Fatal("capture does not establish read-only provenance")
	}
	for i, file := range plan.Files {
		source := captureReceipt.Files[i]
		if source.SourcePath == "" || source.SnapshotPath != file.Path || source.SHA256 != file.SHA256 || source.Bytes != file.Bytes {
			t.Fatal("plan does not match its source provenance")
		}
		body, err := readSnapshotFile(ctx, root, file.Path, maxProbeFileBytes)
		if err != nil || body.receipt.SHA256 != file.SHA256 || body.receipt.Bytes != file.Bytes {
			t.Fatalf("captured input mismatch: %v", err)
		}
	}
	before := sourceFiles(t, root)
	options := Options{SnapshotRoot: root, CatalogFile: plan.CatalogFile, ManagedRoot: plan.ManagedRoot, Destination: plan.Destination}
	first, err := Import(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Import(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if first.ImportID != second.ImportID || first.ImportedAt != second.ImportedAt || !second.AlreadyImported {
		t.Fatal("captured-state import is not idempotent")
	}
	archive, err := OpenReadOnly(ctx, plan.Destination)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := archive.Read(ctx, first.ImportID)
	if err != nil {
		archive.Close()
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshot.CatalogJSON, before[first.Manifest.Catalog.Path].bytes) {
		t.Fatal("normal Go archive reader changed the source catalog")
	}
	compared := 0
	for _, record := range snapshot.Records {
		if !bytes.Equal(record.RawJSON, before[record.Path].bytes) {
			t.Fatal("normal Go archive reader changed a retained lease")
		}
		compared++
	}
	if compared != len(first.Manifest.Leases) {
		t.Fatal("retained lease count differs")
	}
	assertUnchanged(t, before)
	evidence := map[string]any{
		"schema_version": 1, "captured_source_manifest_sha256": plan.CaptureSHA,
		"import_receipt": first, "second_import_reused": second.AlreadyImported,
		"catalog_bytes_equal": true, "lease_documents_compared": compared,
		"all_original_fields_and_versions_preserved": true,
		"source_bytes_and_mtimes_unchanged":          true, "active_lease_adoptions": 0,
		"normal_archive_reader": true, "managed_runtime_migration_proven": false,
	}
	raw, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(plan.Destination, "legacy-workspace-proof-"+first.ImportID+".json")
	if info, statErr := os.Lstat(evidencePath); statErr == nil && !info.Mode().IsRegular() {
		t.Fatal("existing proof receipt is not a regular private file")
	} else if statErr != nil && !os.IsNotExist(statErr) {
		t.Fatal(statErr)
	}
	if existing, readErr := os.ReadFile(evidencePath); readErr == nil {
		var recorded map[string]any
		if json.Unmarshal(existing, &recorded) != nil {
			t.Fatal("existing proof receipt is invalid")
		}
		// A repeat import can change already_imported in the receipt; preserve
		// the first completed receipt rather than overwriting earlier evidence.
		var current map[string]any
		if json.Unmarshal(raw, &current) != nil {
			t.Fatal("new proof receipt is invalid")
		}
		for _, value := range []map[string]any{recorded, current} {
			receipt, ok := value["import_receipt"].(map[string]any)
			if !ok {
				t.Fatal("proof receipt lacks its import receipt")
			}
			receipt["already_imported"] = false
		}
		if !equalJSON(recorded, current) {
			t.Fatal("existing proof receipt differs from the current verified import")
		}
	} else if os.IsNotExist(readErr) {
		file, writeErr := os.OpenFile(evidencePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		_, writeErr = file.Write(append(raw, '\n'))
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	} else {
		t.Fatal(readErr)
	}
	t.Logf("retained catalog/lease archive: documents=%d v1=%d v2=%d idempotent=true active_adoptions=0 proof=%s", compared, first.Manifest.Versions["1"], first.Manifest.Versions["2"], evidencePath)
}

const maxProbeFileBytes = 1 << 20
