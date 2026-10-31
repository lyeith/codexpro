package legacyworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const (
	maxCatalogBytes  = 1_000_000
	maxLeaseBytes    = 1 << 20
	maxIdentityBytes = 4096
	maxEntries       = 10_000
	maxTotalBytes    = 64 << 20
)

type capturedFile struct {
	receipt File
	bytes   []byte
	info    os.FileInfo
}

type captured struct {
	manifest Manifest
	catalog  capturedFile
	identity *capturedFile
	records  []Record
	files    []capturedFile
	entries  []Ignored
	leaseDir string
}

func inside(root, child string) bool {
	relative, err := filepath.Rel(root, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func snapshotPath(root, value string) (string, error) {
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	value = filepath.Clean(value)
	if !inside(root, value) {
		return "", fmt.Errorf("snapshot path escapes its explicit root: %s", value)
	}
	relative, _ := filepath.Rel(root, value)
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("snapshot paths must not contain symlinks: %s", current)
		}
	}
	return value, nil
}

func fileStateEqual(a, b os.FileInfo) bool {
	if !os.SameFile(a, b) || a.Size() != b.Size() || a.Mode() != b.Mode() || !a.ModTime().Equal(b.ModTime()) {
		return false
	}
	x, xok := a.Sys().(*syscall.Stat_t)
	y, yok := b.Sys().(*syscall.Stat_t)
	return !xok || !yok || x.Ctim == y.Ctim && x.Nlink == y.Nlink
}

func readSnapshotFile(ctx context.Context, root, path string, limit int64) (capturedFile, error) {
	if err := ctx.Err(); err != nil {
		return capturedFile{}, err
	}
	canonical, err := snapshotPath(root, path)
	if err != nil {
		return capturedFile{}, err
	}
	fd, err := syscall.Open(canonical, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return capturedFile{}, err
	}
	file := os.NewFile(uintptr(fd), canonical)
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return capturedFile{}, err
	}
	if !before.Mode().IsRegular() {
		return capturedFile{}, fmt.Errorf("snapshot entry must be a regular file: %s", canonical)
	}
	if stat, ok := before.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
		return capturedFile{}, fmt.Errorf("snapshot file must be an independent copy: %s", canonical)
	}
	if before.Size() > limit {
		return capturedFile{}, fmt.Errorf("snapshot file exceeds %d bytes: %s", limit, canonical)
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return capturedFile{}, err
	}
	if int64(len(raw)) > limit {
		return capturedFile{}, fmt.Errorf("snapshot file exceeds %d bytes: %s", limit, canonical)
	}
	after, err := file.Stat()
	if err != nil {
		return capturedFile{}, err
	}
	current, err := os.Lstat(canonical)
	if err != nil || !fileStateEqual(before, after) || !fileStateEqual(after, current) {
		return capturedFile{}, fmt.Errorf("snapshot file changed while being read: %s", canonical)
	}
	if err := ctx.Err(); err != nil {
		return capturedFile{}, err
	}
	return capturedFile{receipt: File{Path: canonical, SHA256: hash(raw), Bytes: int64(len(raw))}, bytes: raw, info: after}, nil
}

func directoryEntries(path string) ([]os.DirEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries := []os.DirEntry{}
	for {
		batch, readErr := file.ReadDir(128)
		entries = append(entries, batch...)
		if len(entries) > maxEntries {
			return nil, fmt.Errorf("snapshot lease directory exceeds %d entries", maxEntries)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func entryInventory(entries []os.DirEntry) []Ignored {
	result := make([]Ignored, 0, len(entries))
	for _, entry := range entries {
		kind := "regular"
		if entry.Type()&os.ModeSymlink != 0 {
			kind = "symlink"
		} else if entry.IsDir() {
			kind = "directory"
		} else if !entry.Type().IsRegular() {
			kind = "non-regular"
		}
		result = append(result, Ignored{Path: entry.Name(), Reason: kind})
	}
	return result
}

func capture(ctx context.Context, options Options) (captured, error) {
	root, err := filepath.Abs(options.SnapshotRoot)
	if err != nil {
		return captured{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return captured{}, err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return captured{}, fmt.Errorf("snapshot root is not an existing directory")
	}
	catalogPath, err := snapshotPath(root, options.CatalogFile)
	if err != nil {
		return captured{}, err
	}
	managed, err := snapshotPath(root, options.ManagedRoot)
	if err != nil {
		return captured{}, err
	}
	leaseDir, err := snapshotPath(root, filepath.Join(managed, "leases"))
	if err != nil {
		return captured{}, err
	}
	catalog, err := readSnapshotFile(ctx, root, catalogPath, maxCatalogBytes)
	if err != nil {
		return captured{}, err
	}
	projectIDs, err := sourceCatalog(catalog.bytes)
	if err != nil {
		return captured{}, err
	}
	result := captured{
		catalog: catalog, files: []capturedFile{catalog}, records: []Record{}, leaseDir: leaseDir,
		manifest: Manifest{
			SchemaVersion: SchemaVersion, SnapshotRoot: root, ManagedRoot: managed,
			Catalog: catalog.receipt, ProjectIDs: projectIDs, Leases: []Lease{},
			Ignored: []Ignored{}, States: map[string]int{}, Versions: map[string]int{},
		},
	}
	total := catalog.receipt.Bytes
	identityPath := filepath.Join(managed, "connector-id")
	if _, err := os.Lstat(identityPath); err == nil {
		identity, err := readSnapshotFile(ctx, root, identityPath, maxIdentityBytes)
		if err != nil {
			return captured{}, err
		}
		if err = sourceIdentity(identity.bytes); err != nil {
			return captured{}, err
		}
		result.identity = &identity
		result.manifest.Identity = &identity.receipt
		result.files = append(result.files, identity)
		total += identity.receipt.Bytes
	} else if !os.IsNotExist(err) {
		return captured{}, err
	}
	entries, err := directoryEntries(leaseDir)
	if err != nil {
		return captured{}, err
	}
	result.entries = entryInventory(entries)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return captured{}, err
		}
		path := filepath.Join(leaseDir, entry.Name())
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			reason := "not a regular .json file; ignored by FileLeaseStore"
			result.manifest.Ignored = append(result.manifest.Ignored, Ignored{Path: path, Reason: reason})
			continue
		}
		file, err := readSnapshotFile(ctx, root, path, maxLeaseBytes)
		if err != nil {
			return captured{}, err
		}
		total += file.receipt.Bytes
		if total > maxTotalBytes {
			return captured{}, fmt.Errorf("snapshot contents exceed %d bytes", maxTotalBytes)
		}
		lease, err := SourceLease(file.bytes, entry.Name())
		if err != nil {
			// The reference quarantines this file. The read-only importer cannot
			// rename its source, so reject the complete import before opening SQL.
			return captured{}, fmt.Errorf("invalid snapshot lease %s (source left unchanged): %w", entry.Name(), err)
		}
		lease.File = file.receipt
		result.records = append(result.records, Record{Lease: lease, RawJSON: file.bytes})
		result.files = append(result.files, file)
		result.manifest.Leases = append(result.manifest.Leases, lease)
		result.manifest.States[lease.State]++
		result.manifest.Versions[strconv.Itoa(lease.Version)]++
	}
	if err = result.verify(ctx); err != nil {
		return captured{}, err
	}
	return result, nil
}

func (snapshot captured) verify(ctx context.Context) error {
	for _, original := range snapshot.files {
		current, err := readSnapshotFile(ctx, snapshot.manifest.SnapshotRoot, original.receipt.Path, original.receipt.Bytes+1)
		if err != nil {
			return err
		}
		if !fileStateEqual(original.info, current.info) || !bytes.Equal(original.bytes, current.bytes) {
			return fmt.Errorf("snapshot changed during import preparation: %s", original.receipt.Path)
		}
	}
	entries, err := directoryEntries(snapshot.leaseDir)
	if err != nil {
		return err
	}
	if !equalJSON(snapshot.entries, entryInventory(entries)) {
		return fmt.Errorf("snapshot lease directory changed during import preparation")
	}
	if snapshot.identity == nil {
		if _, err := os.Lstat(filepath.Join(snapshot.manifest.ManagedRoot, "connector-id")); !os.IsNotExist(err) {
			return fmt.Errorf("snapshot connector identity changed during import preparation")
		}
	}
	return nil
}
