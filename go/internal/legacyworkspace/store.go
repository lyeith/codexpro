package legacyworkspace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	_ "github.com/mattn/go-sqlite3"
)

const DatabaseName = "legacy-workspaces.sqlite"

type Archive struct {
	db   *sql.DB
	path string
}

func privateDatabase(directory string) (string, error) {
	if directory == "" {
		return "", errors.New("an explicit private destination directory is required")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	directory = filepath.Clean(directory)
	path := filepath.Join(directory, DatabaseName)
	guard := config.New(directory)
	// These configuration fields are only path-guard inputs. No token/catalog
	// reader is called and no ordinary service store is initialized.
	guard.ProjectsFile = path
	guard.TokenFile = path + "-wal"
	guard.EnvFile = path + "-shm"
	if err := guard.ValidateIsolation(); err != nil {
		return "", err
	}
	for _, name := range []string{path, path + "-wal", path + "-shm", path + "-journal", filepath.Join(directory, "legacy-workspaces.lock")} {
		info, err := os.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("private archive state must be a regular file without symlinks: %s", name)
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
			return "", fmt.Errorf("private archive state must not share hardlinks: %s", name)
		}
	}
	return path, nil
}

func openDatabase(ctx context.Context, path string, readOnly bool) (*sql.DB, error) {
	mode := "rwc"
	if readOnly {
		mode = "ro"
	}
	query := url.Values{"mode": []string{mode}, "_busy_timeout": []string{"5000"}}
	if !readOnly {
		query.Set("_journal_mode", "WAL")
		query.Set("_synchronous", "FULL")
		query.Set("_foreign_keys", "on")
	}
	location := url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if readOnly {
		if _, err = db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func lockArchive(directory string) (*os.File, error) {
	path := filepath.Join(directory, "legacy-workspaces.lock")
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("private archive lock is not a regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
		file.Close()
		return nil, fmt.Errorf("private archive lock must not share hardlinks")
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another importer owns the private archive lock: %w", err)
	}
	return file, nil
}

// Import creates a provenance-preserving archive, not a runtime workspace store.
// It never calls FileLeaseStore.initialize, Git, Manager.loadLeases, a worker, or
// a process-control API. Invalid source state fails before any SQL is opened.
func Import(ctx context.Context, options Options) (Receipt, error) {
	if options.SnapshotRoot == "" || options.CatalogFile == "" || options.ManagedRoot == "" {
		return Receipt{}, errors.New("snapshot root, catalog file and managed root are required")
	}
	snapshot, err := capture(ctx, options)
	if err != nil {
		return Receipt{}, err
	}
	path, err := privateDatabase(options.Destination)
	if err != nil {
		return Receipt{}, err
	}
	directory := filepath.Dir(path)
	// Even an ordinary private destination may not overlap the immutable input.
	// Resolve its nearest existing ancestor before creating directories.
	canonicalDestination, err := futurePath(directory)
	if err != nil {
		return Receipt{}, err
	}
	if inside(snapshot.manifest.SnapshotRoot, canonicalDestination) || inside(canonicalDestination, snapshot.manifest.SnapshotRoot) {
		return Receipt{}, errors.New("archive destination must not overlap the immutable snapshot root")
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return Receipt{}, err
	}
	canonicalDestination, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return Receipt{}, err
	}
	path, err = privateDatabase(canonicalDestination)
	if err != nil {
		return Receipt{}, err
	}
	lock, err := lockArchive(canonicalDestination)
	if err != nil {
		return Receipt{}, err
	}
	defer lock.Close()
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if createErr != nil {
			return Receipt{}, createErr
		}
		if err = file.Close(); err != nil {
			return Receipt{}, err
		}
	} else if err != nil {
		return Receipt{}, err
	}
	db, err := openDatabase(ctx, path, false)
	if err != nil {
		return Receipt{}, err
	}
	defer db.Close()
	if err = initialize(ctx, db); err != nil {
		return Receipt{}, err
	}
	id, err := importID(snapshot.manifest)
	if err != nil {
		return Receipt{}, err
	}
	archive := &Archive{db: db, path: path}
	existing, err := archive.Read(ctx, id)
	if err == nil {
		if !equalJSON(existing.Manifest, snapshot.manifest) || !bytes.Equal(existing.CatalogJSON, snapshot.catalog.bytes) || len(existing.Records) != len(snapshot.records) {
			return Receipt{}, errors.New("existing legacy import differs from its source manifest")
		}
		for i := range existing.Records {
			if !bytes.Equal(existing.Records[i].RawJSON, snapshot.records[i].RawJSON) {
				return Receipt{}, errors.New("existing legacy lease differs from its source bytes")
			}
		}
		if snapshot.identity != nil && !bytes.Equal(existing.Identity, snapshot.identity.bytes) {
			return Receipt{}, errors.New("existing legacy connector identity differs from its source bytes")
		}
		if err = snapshot.verify(ctx); err != nil {
			return Receipt{}, err
		}
		result := existing.Receipt
		result.AlreadyImported = true
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, err
	}
	receipt := Receipt{
		SchemaVersion: SchemaVersion, ImportID: id, ImportedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Destination: path, ReadOnlyHistory: true, Manifest: snapshot.manifest,
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return Receipt{}, err
	}
	if err = snapshot.verify(ctx); err != nil {
		return Receipt{}, err
	}
	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, err
	}
	defer transaction.Rollback()
	if _, err = transaction.ExecContext(ctx, "INSERT INTO legacy_imports(id,receipt)VALUES(?,?)", id, body); err != nil {
		return Receipt{}, err
	}
	var identity any
	if snapshot.identity != nil {
		identity = snapshot.identity.bytes
	}
	if _, err = transaction.ExecContext(ctx, "INSERT INTO legacy_catalogs(import_id,raw_json,identity)VALUES(?,?,?)", id, snapshot.catalog.bytes, identity); err != nil {
		return Receipt{}, err
	}
	for ordinal, record := range snapshot.records {
		var project any
		if record.ProjectID != nil {
			project = *record.ProjectID
		}
		if _, err = transaction.ExecContext(ctx, "INSERT INTO legacy_leases(import_id,ordinal,workspace_id,project_id,source_version,state,raw_json)VALUES(?,?,?,?,?,?,?)",
			id, ordinal, record.WorkspaceID, project, record.Version, record.State, record.RawJSON); err != nil {
			return Receipt{}, err
		}
	}
	if err = transaction.Commit(); err != nil {
		return Receipt{}, err
	}
	// Commit durability comes from SQLite FULL; sync the containing directory
	// as well because this may be the first creation of the archive database.
	parent, err := os.Open(canonicalDestination)
	if err != nil {
		return Receipt{}, err
	}
	syncErr := parent.Sync()
	closeErr := parent.Close()
	if syncErr != nil {
		return Receipt{}, syncErr
	}
	if closeErr != nil {
		return Receipt{}, closeErr
	}
	return receipt, nil
}

func futurePath(path string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", fmt.Errorf("could not resolve destination ancestor")
	}
	resolved, err := futurePath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

func initialize(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 0 && version != SchemaVersion {
		return fmt.Errorf("unsupported legacy workspace archive version %d", version)
	}
	if version == 0 {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("refusing to initialize a nonempty unversioned database")
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS legacy_imports(id TEXT PRIMARY KEY,receipt BLOB NOT NULL)",
		"CREATE TABLE IF NOT EXISTS legacy_catalogs(import_id TEXT PRIMARY KEY REFERENCES legacy_imports(id),raw_json BLOB NOT NULL,identity BLOB)",
		"CREATE TABLE IF NOT EXISTS legacy_leases(import_id TEXT NOT NULL REFERENCES legacy_imports(id),ordinal INTEGER NOT NULL,workspace_id TEXT NOT NULL,project_id TEXT,source_version INTEGER NOT NULL,state TEXT NOT NULL,raw_json BLOB NOT NULL,PRIMARY KEY(import_id,ordinal),UNIQUE(import_id,workspace_id))",
		"PRAGMA user_version=1",
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func OpenReadOnly(ctx context.Context, directory string) (*Archive, error) {
	path, err := privateDatabase(directory)
	if err != nil {
		return nil, err
	}
	db, err := openDatabase(ctx, path, true)
	if err != nil {
		return nil, err
	}
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		db.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("unsupported legacy workspace archive version %d", version)
	}
	return &Archive{db: db, path: path}, nil
}

func (archive *Archive) Close() error { return archive.db.Close() }

func (archive *Archive) Imports(ctx context.Context) ([]string, error) {
	rows, err := archive.db.QueryContext(ctx, "SELECT id FROM legacy_imports ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (archive *Archive) Read(ctx context.Context, id string) (Snapshot, error) {
	var result Snapshot
	var raw []byte
	if err := archive.db.QueryRowContext(ctx, "SELECT receipt FROM legacy_imports WHERE id=?", id).Scan(&raw); err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &result.Receipt); err != nil {
		return result, fmt.Errorf("corrupt legacy import receipt: %w", err)
	}
	expectedID, err := importID(result.Manifest)
	if err != nil || expectedID != id || result.ImportID != id || result.SchemaVersion != SchemaVersion || result.Manifest.SchemaVersion != SchemaVersion || !result.ReadOnlyHistory || result.ActiveAdoptions != 0 {
		return result, errors.New("legacy import receipt failed integrity validation")
	}
	if err := archive.db.QueryRowContext(ctx, "SELECT raw_json,identity FROM legacy_catalogs WHERE import_id=?", id).Scan(&result.CatalogJSON, &result.Identity); err != nil {
		return result, err
	}
	if hash(result.CatalogJSON) != result.Manifest.Catalog.SHA256 || int64(len(result.CatalogJSON)) != result.Manifest.Catalog.Bytes {
		return result, errors.New("legacy catalog failed integrity validation")
	}
	if result.Manifest.Identity == nil {
		if result.Identity != nil {
			return result, errors.New("unexpected legacy connector identity")
		}
	} else if hash(result.Identity) != result.Manifest.Identity.SHA256 || int64(len(result.Identity)) != result.Manifest.Identity.Bytes {
		return result, errors.New("legacy connector identity failed integrity validation")
	}
	rows, err := archive.db.QueryContext(ctx, "SELECT ordinal,workspace_id,project_id,source_version,state,raw_json FROM legacy_leases WHERE import_id=? ORDER BY ordinal", id)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	result.Records = []Record{}
	for rows.Next() {
		var ordinal, version int
		var workspace, state string
		var project sql.NullString
		var body []byte
		if err = rows.Scan(&ordinal, &workspace, &project, &version, &state, &body); err != nil {
			return result, err
		}
		if ordinal != len(result.Records) || ordinal >= len(result.Manifest.Leases) {
			return result, errors.New("legacy lease ordinals failed integrity validation")
		}
		expected := result.Manifest.Leases[ordinal]
		if expected.WorkspaceID != workspace || expected.Version != version || expected.State != state ||
			(expected.ProjectID != nil) != project.Valid || project.Valid && *expected.ProjectID != project.String ||
			expected.SHA256 != hash(body) || expected.Bytes != int64(len(body)) {
			return result, errors.New("legacy lease failed integrity validation")
		}
		result.Records = append(result.Records, Record{Lease: expected, RawJSON: body})
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Records) != len(result.Manifest.Leases) {
		return result, errors.New("legacy lease count failed integrity validation")
	}
	return result, nil
}
