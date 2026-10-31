package legacyworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) Options {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "snapshot")
	managed := filepath.Join(root, "worktrees")
	leases := filepath.Join(managed, "leases")
	if err := os.MkdirAll(leases, 0700); err != nil {
		t.Fatal(err)
	}
	catalog := []byte("{\n  \"version\": 1, \"defaultProject\": \"fixture\", \"projects\": [{\"id\": \"fixture\", \"root\": \"/recorded/offline-repository\", \"label\": \"Fixture\", \"baseRef\": \"main\", \"maxWorktrees\": 7}], \"creationRoots\": null\n}\n")
	if err := os.WriteFile(filepath.Join(root, "projects.json"), catalog, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managed, "connector-id"), []byte(strings.Repeat("a", 48)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 2; version++ {
		for _, state := range []string{"provisioning", "ready", "removing", "orphaned", "failed"} {
			id := state + "-" + string(rune('0'+version))
			value := map[string]any{
				"version": version, "revision": 9, "workspaceId": id,
				"repositoryId": "recorded-repository", "repositoryCommonDir": "/recorded/offline-repository/.git",
				"checkoutRoot": "/recorded/offline-worktrees/" + id, "workspaceRoot": "/recorded/offline-worktrees/" + id + "/scope",
				"branch": "codexpro/" + id, "baseCommit": strings.Repeat("b", 40),
				"ownerId": "connector_preserved_owner", "state": state,
				"createdAt": "2026-10-31T01:00:00.000Z", "updatedAt": "2026-10-31T02:00:00.000Z", "lastUsedAt": "2026-10-31T03:00:00.000Z",
				"releasedAt": nil, "futureField": map[string]any{"array": []any{nil, false, "preserved"}},
			}
			if version == 2 {
				value["projectId"] = "fixture"
			}
			raw, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, '\n')
			if err := os.WriteFile(filepath.Join(leases, hash([]byte(id))+".json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(leases, "old.json.invalid-123"), []byte("source quarantine left unchanged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return Options{SnapshotRoot: root, CatalogFile: "projects.json", ManagedRoot: "worktrees", Destination: filepath.Join(base, "archive")}
}

func sourceFiles(t *testing.T, root string) map[string]capturedFile {
	t.Helper()
	result := map[string]capturedFile{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			result[path] = capturedFile{bytes: raw, info: info}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertUnchanged(t *testing.T, before map[string]capturedFile) {
	t.Helper()
	for path, previous := range before {
		current, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(previous.bytes, current) || !fileStateEqual(previous.info, info) {
			t.Fatalf("import changed source bytes or metadata: %s", path)
		}
	}
}

func TestLegacyImportPreservesBytesVersionsHistoryAndIdempotency(t *testing.T) {
	options := fixture(t)
	before := sourceFiles(t, options.SnapshotRoot)
	receipt, err := Import(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.AlreadyImported || !receipt.ReadOnlyHistory || receipt.ActiveAdoptions != 0 ||
		receipt.Manifest.CatalogRootsResolved || len(receipt.Manifest.Leases) != 10 ||
		receipt.Manifest.Versions["1"] != 5 || receipt.Manifest.Versions["2"] != 5 ||
		len(receipt.Manifest.Ignored) != 1 {
		t.Fatalf("incomplete or unsafe import receipt: %+v", receipt)
	}
	for _, state := range []string{"provisioning", "ready", "removing", "orphaned", "failed"} {
		if receipt.Manifest.States[state] != 2 {
			t.Fatalf("lost state %s", state)
		}
	}
	again, err := Import(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !again.AlreadyImported || receipt.ImportID != again.ImportID || receipt.ImportedAt != again.ImportedAt {
		t.Fatalf("import is not idempotent: %+v", again)
	}
	archive, err := OpenReadOnly(context.Background(), options.Destination)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	snapshot, err := archive.Read(context.Background(), receipt.ImportID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshot.CatalogJSON, before[receipt.Manifest.Catalog.Path].bytes) {
		t.Fatal("catalog bytes changed")
	}
	for _, record := range snapshot.Records {
		if !bytes.Equal(record.RawJSON, before[record.Path].bytes) {
			t.Fatal("lease bytes changed")
		}
		if record.OwnerID != "connector_preserved_owner" {
			t.Fatal("owner was remapped")
		}
		if record.Version == 1 && (record.ProjectID != nil || record.HasProjectID) {
			t.Fatal("v1 lease was silently promoted")
		}
	}
	imports, err := archive.Imports(context.Background())
	if err != nil || len(imports) != 1 {
		t.Fatalf("duplicate import: %v %v", imports, err)
	}
	var activeTables int
	err = archive.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('leases','jobs','job_inputs')").Scan(&activeTables)
	if err != nil || activeTables != 0 {
		t.Fatalf("archive contains runtime ownership tables: %d %v", activeTables, err)
	}
	if _, err = archive.db.Exec("CREATE TABLE forbidden_mutation(id INTEGER)"); err == nil {
		t.Fatal("archive reader can write")
	}
	info, err := os.Stat(filepath.Join(options.Destination, DatabaseName))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("archive mode is not private: %v", err)
	}
	assertUnchanged(t, before)
}

func TestChangedSnapshotAddsHistoryWithoutReplacingPriorImport(t *testing.T) {
	options := fixture(t)
	first, err := Import(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	path := first.Manifest.Leases[0].Path
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := append([]byte(" \n"), original...)
	if err = os.WriteFile(path, modified, 0600); err != nil {
		t.Fatal(err)
	}
	second, err := Import(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if second.AlreadyImported || second.ImportID == first.ImportID {
		t.Fatal("changed snapshot replaced an import")
	}
	archive, err := OpenReadOnly(context.Background(), options.Destination)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	old, err := archive.Read(context.Background(), first.ImportID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(old.Records[0].RawJSON, original) {
		t.Fatal("prior history was overwritten")
	}
	imports, err := archive.Imports(context.Background())
	if err != nil || len(imports) != 2 {
		t.Fatalf("history count: %v %v", imports, err)
	}
}

func TestInvalidSourceFailsWithoutQuarantineOrDestination(t *testing.T) {
	options := fixture(t)
	bad := filepath.Join(options.SnapshotRoot, "worktrees", "leases", "invalid.json")
	if err := os.WriteFile(bad, []byte("{broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := sourceFiles(t, options.SnapshotRoot)
	if _, err := Import(context.Background(), options); err == nil {
		t.Fatal("invalid source was accepted")
	}
	if _, err := os.Stat(options.Destination); !os.IsNotExist(err) {
		t.Fatalf("invalid source created a destination: %v", err)
	}
	assertUnchanged(t, before)
}

func TestInputAliasesAndDestinationOverlapAreRejected(t *testing.T) {
	t.Run("catalog-symlink", func(t *testing.T) {
		options := fixture(t)
		catalog := filepath.Join(options.SnapshotRoot, options.CatalogFile)
		target := filepath.Join(filepath.Dir(options.SnapshotRoot), "external.json")
		if err := os.Rename(catalog, target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, catalog); err != nil {
			t.Fatal(err)
		}
		if _, err := Import(context.Background(), options); err == nil {
			t.Fatal("catalog symlink followed")
		}
	})
	t.Run("hardlinked-source", func(t *testing.T) {
		options := fixture(t)
		catalog := filepath.Join(options.SnapshotRoot, options.CatalogFile)
		if err := os.Link(catalog, filepath.Join(filepath.Dir(options.SnapshotRoot), "alias.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := Import(context.Background(), options); err == nil {
			t.Fatal("non-independent source accepted")
		}
	})
	t.Run("overlap", func(t *testing.T) {
		options := fixture(t)
		options.Destination = filepath.Join(options.SnapshotRoot, "archive")
		if _, err := Import(context.Background(), options); err == nil {
			t.Fatal("destination overlaps immutable snapshot")
		}
		if _, err := os.Stat(options.Destination); !os.IsNotExist(err) {
			t.Fatal("overlapping destination was created")
		}
	})
	t.Run("forbidden-live-destination", func(t *testing.T) {
		options := fixture(t)
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		options.Destination = filepath.Join(home, ".codexpro", "sp6-import-must-not-exist")
		if _, err := Import(context.Background(), options); err == nil {
			t.Fatal("live destination accepted")
		}
	})
}

func TestSourceChangeAndCancellationAreExplicit(t *testing.T) {
	options := fixture(t)
	snapshot, err := capture(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(snapshot.catalog.receipt.Path, append(snapshot.catalog.bytes, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if err = snapshot.verify(context.Background()); err == nil {
		t.Fatal("changed snapshot went undetected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Import(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	if _, err = os.Stat(options.Destination); !os.IsNotExist(err) {
		t.Fatal("canceled import created a destination")
	}
}

func TestArchiveCorruptionIsReportedWithoutRepairingIt(t *testing.T) {
	options := fixture(t)
	receipt, err := Import(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	db, err := openDatabase(context.Background(), filepath.Join(options.Destination, DatabaseName), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE legacy_catalogs SET raw_json=? WHERE import_id=?", []byte("{}"), receipt.ImportID); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = Import(context.Background(), options); err == nil {
		t.Fatal("corrupt archive was silently repaired")
	}
	archive, err := OpenReadOnly(context.Background(), options.Destination)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if _, err = archive.Read(context.Background(), receipt.ImportID); err == nil {
		t.Fatal("corrupt archive was exposed as valid")
	}
}
