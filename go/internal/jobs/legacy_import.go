package jobs

// This importer is deliberately offline. Its input is a stable private copy of
// the TypeScript v1 jobs directory, never a live JobManager or a set of PIDs to
// adopt. Historical process identities remain only in the provenance archive.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/process"
)

const legacyImportMarker = ".legacy-job-import.in-progress"
const legacyImportManifest = "legacy-jobs/manifest.json"
const legacyImportMetadataLimit int64 = 64 << 20
const legacyImportSafeInteger int64 = 9007199254740991

var legacyJobID = regexp.MustCompile(`^job_[0-9a-f]{8}$`)
var legacySHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

type LegacyImportOptions struct {
	// SourceDir is an already-copied private directory containing jobs.json and
	// fixed ID-derived output paths. OriginalJobsDir only validates provenance;
	// no file beneath OriginalJobsDir is opened by this importer.
	SourceDir              string
	OriginalJobsDir        string
	ExpectedMetadataSHA256 string
	MaxRecords             int
	MaxBytes               int64
	MaxFileBytes           int64
}

type LegacyImportFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type LegacyImportJob struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	Status        string `json:"status"`
	Disposition   string `json:"disposition"`
	Reason        string `json:"reason,omitempty"`
	OutputExpired bool   `json:"output_expired"`
	RawBytes      int64  `json:"raw_bytes"`
	RenderedBytes int64  `json:"rendered_bytes"`
}

type LegacyImportReport struct {
	Version            int                `json:"version"`
	SourceVersion      int                `json:"source_version"`
	DestinationVersion int                `json:"destination_version"`
	SourceDir          string             `json:"source_dir"`
	OriginalJobsDir    string             `json:"original_jobs_dir"`
	Destination        string             `json:"destination"`
	MetadataSHA256     string             `json:"metadata_sha256"`
	ImportedAt         string             `json:"imported_at"`
	SourceRecords      int                `json:"source_records"`
	ImportedRecords    int                `json:"imported_records"`
	ArchiveOnlyRecords int                `json:"archive_only_records"`
	ExpiredRecords     int                `json:"expired_records"`
	RawBytes           int64              `json:"raw_bytes"`
	RenderedBytes      int64              `json:"rendered_bytes"`
	Files              []LegacyImportFile `json:"files"`
	Jobs               []LegacyImportJob  `json:"jobs"`
	ArchivePath        string             `json:"archive_path"`
	Replayed           bool               `json:"replayed"`
}

type legacyTable struct {
	Version int               `json:"version"`
	Jobs    []json.RawMessage `json:"jobs"`
}

// A shared lock across construction closes the mkdir/marker race with the
// offline importer. Import holds an exclusive lock on the same directory. The
// normal service does not keep this lock after construction; subsequent import
// attempts can only replay an immutable receipt, never overwrite existing state.
func lockJobsHome(home string, exclusive bool) (*os.File, error) {
	if !exclusive {
		if err := os.MkdirAll(home, 0700); err != nil {
			return nil, err
		}
	}
	fd, err := syscall.Open(home, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), home)
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err = syscall.Flock(fd, mode|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("job state is being opened or imported: %w", err)
	}
	if !exclusive {
		if _, err = os.Lstat(filepath.Join(home, legacyImportMarker)); err == nil {
			f.Close()
			return nil, errors.New("job history import is incomplete; retain its private quarantine and use a fresh destination")
		} else if !os.IsNotExist(err) {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

func normalizeLegacyImport(cfg *config.Config, o LegacyImportOptions) (LegacyImportOptions, string, error) {
	if cfg == nil {
		return o, "", errors.New("legacy job import requires private configuration")
	}
	if err := cfg.ValidateIsolation(); err != nil {
		return o, "", err
	}
	if !filepath.IsAbs(cfg.Home) || filepath.Clean(cfg.Home) != cfg.Home {
		return o, "", errors.New("import destination must be an absolute clean path")
	}
	if !filepath.IsAbs(o.SourceDir) || !filepath.IsAbs(o.OriginalJobsDir) {
		return o, "", errors.New("private source and original jobs directory must be absolute paths")
	}
	if !legacySHA256.MatchString(o.ExpectedMetadataSHA256) {
		return o, "", errors.New("expected jobs.json SHA-256 is required")
	}
	source, err := filepath.EvalSymlinks(o.SourceDir)
	if err != nil {
		return o, "", err
	}
	if source != filepath.Clean(o.SourceDir) {
		return o, "", errors.New("private snapshot directory must not traverse symlinks")
	}
	if err = config.New(source).ValidateIsolation(); err != nil {
		return o, "", fmt.Errorf("source must be a private snapshot: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(cfg.Home))
	if err != nil {
		return o, "", err
	}
	home := filepath.Join(parent, filepath.Base(cfg.Home))
	if home != cfg.Home {
		return o, "", errors.New("import destination ancestors must not traverse symlinks")
	}
	if process.Within(source, home) || process.Within(home, source) {
		return o, "", errors.New("snapshot and import destination must be separate directories")
	}
	o.SourceDir, o.OriginalJobsDir = source, filepath.Clean(o.OriginalJobsDir)
	if o.MaxRecords == 0 {
		o.MaxRecords = 10000
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 512 << 20
	}
	if o.MaxFileBytes == 0 {
		o.MaxFileBytes = 64 << 20
	}
	if o.MaxRecords < 1 || o.MaxRecords > 100000 || o.MaxBytes < 1 || o.MaxBytes > 2<<30 || o.MaxFileBytes < 1 || o.MaxFileBytes > 256<<20 || o.MaxFileBytes > o.MaxBytes {
		return o, "", errors.New("legacy job import bounds are invalid")
	}
	return o, home, nil
}

// openLegacyFile walks every snapshot component using openat/O_NOFOLLOW. A
// private copy cannot supply a symlink, FIFO, device, socket or hard link as an
// output file, even if the original record contains an arbitrary absolute path.
func openLegacyFile(root, relative string) (*os.File, error) {
	if filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == "." || strings.HasPrefix(relative, "..") {
		return nil, errors.New("invalid snapshot-relative file path")
	}
	fd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		next, e := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK | syscall.O_NOATIME
	fileFD, err := syscall.Openat(fd, parts[len(parts)-1], flags, 0)
	// NOATIME is not supported for files owned by another user; reads still
	// never write the source's data, permissions, mtime or persistence metadata.
	if errors.Is(err, syscall.EPERM) {
		fileFD, err = syscall.Openat(fd, parts[len(parts)-1], flags&^syscall.O_NOATIME, 0)
	}
	syscall.Close(fd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fileFD), filepath.Join(root, relative))
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 {
		f.Close()
		return nil, errors.New("snapshot files must be regular private copies without hard links")
	}
	return f, nil
}

func sameLegacyFile(a, b os.FileInfo) bool {
	x, xok := a.Sys().(*syscall.Stat_t)
	y, yok := b.Sys().(*syscall.Stat_t)
	return xok && yok && x.Dev == y.Dev && x.Ino == y.Ino && x.Size == y.Size && x.Mtim == y.Mtim && x.Ctim == y.Ctim && x.Nlink == y.Nlink
}

func readLegacyFile(ctx context.Context, root, relative string, limit int64, dst io.Writer) (LegacyImportFile, error) {
	receipt := LegacyImportFile{Path: relative}
	f, err := openLegacyFile(root, relative)
	if err != nil {
		return receipt, fmt.Errorf("snapshot %s: %w", relative, err)
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return receipt, err
	}
	if before.Size() < 0 || before.Size() > limit {
		return receipt, fmt.Errorf("snapshot %s exceeds %d bytes", relative, limit)
	}
	h := sha256.New()
	writer := io.MultiWriter(h, dst)
	buf := make([]byte, 64<<10)
	var copied int64
	for {
		if err = ctx.Err(); err != nil {
			return receipt, err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			copied += int64(n)
			if copied > limit || copied > before.Size() {
				return receipt, fmt.Errorf("snapshot %s changed while reading", relative)
			}
			if _, err = writer.Write(buf[:n]); err != nil {
				return receipt, err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return receipt, readErr
		}
	}
	after, err := f.Stat()
	if err != nil {
		return receipt, err
	}
	current, err := openLegacyFile(root, relative)
	if err != nil {
		return receipt, err
	}
	currentInfo, statErr := current.Stat()
	closeErr := current.Close()
	if statErr != nil {
		return receipt, statErr
	}
	if closeErr != nil {
		return receipt, closeErr
	}
	if copied != before.Size() || !sameLegacyFile(before, after) || !sameLegacyFile(before, currentInfo) {
		return receipt, fmt.Errorf("snapshot %s changed while reading", relative)
	}
	receipt.Bytes, receipt.SHA256 = copied, hex.EncodeToString(h.Sum(nil))
	return receipt, nil
}

func copyLegacyFile(ctx context.Context, source, relative, target string, limit int64, rendered bool) (LegacyImportFile, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return LegacyImportFile{}, err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return LegacyImportFile{}, err
	}
	writer := io.Writer(f)
	validator := &legacyUTF8Writer{writer: f}
	if rendered {
		writer = validator
	}
	receipt, err := readLegacyFile(ctx, source, relative, limit, writer)
	if err == nil && rendered {
		err = validator.finish()
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = process.SyncDir(filepath.Dir(target))
	}
	return receipt, err
}

// Validate rendered UTF-8 while streaming; raw capture is arbitrary bytes.
type legacyUTF8Writer struct {
	writer io.Writer
	tail   []byte
}

func (w *legacyUTF8Writer) Write(b []byte) (int, error) {
	data := append(w.tail, b...)
	i := 0
	for i < len(data) {
		if !utf8.FullRune(data[i:]) {
			break
		}
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			return 0, errors.New("snapshot rendered output is not valid UTF-8")
		}
		i += size
	}
	w.tail = append(w.tail[:0], data[i:]...)
	return w.writer.Write(b)
}
func (w *legacyUTF8Writer) finish() error {
	if len(w.tail) != 0 {
		return errors.New("snapshot rendered output ends inside a UTF-8 character")
	}
	return nil
}

func decodeLegacyRecord(raw json.RawMessage, original string) (Record, bool, string, error) {
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, false, "", err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return r, false, "", err
	}
	if !legacyJobID.MatchString(r.ID) || r.WorkspaceID == "" || len(r.WorkspaceID) > 256 {
		return r, false, "", errors.New("invalid source job identity")
	}
	if r.Origin != "foreground" && r.Origin != "background" && r.Origin != "promoted" {
		return r, false, "", fmt.Errorf("%s: invalid source origin", r.ID)
	}
	switch r.Status {
	case "running", "succeeded", "failed", "stopped", "timed_out":
	default:
		return r, false, "", fmt.Errorf("%s: invalid source status", r.ID)
	}
	started, err := time.Parse(time.RFC3339Nano, r.StartedAt)
	if err != nil || r.StartedAtMS <= 0 || r.StartedAtMS > legacyImportSafeInteger || started.UnixMilli() != r.StartedAtMS {
		return r, false, "", fmt.Errorf("%s: invalid source start time", r.ID)
	}
	if r.StdoutPath != filepath.Join(original, r.ID+".out") || r.StderrPath != filepath.Join(original, r.ID+".err") {
		return r, false, "", fmt.Errorf("%s: source output path does not match declared original jobs directory", r.ID)
	}
	for _, n := range []int64{r.DeadlineMS, r.TimeoutMS, r.OutputLimitBytes, r.CapturedStdoutBytes, r.CapturedStderrBytes} {
		if n < 0 || n > legacyImportSafeInteger {
			return r, false, "", fmt.Errorf("%s: invalid source byte/time count", r.ID)
		}
	}
	if r.Status == "running" {
		return r, false, "active source job; authority remains with TypeScript", nil
	}
	if r.LaunchState == "prepared" {
		return r, false, "ungranted source launch; no adoption or revocation attempted", nil
	}
	if q, present := fields["quiescent"]; present && string(bytes.TrimSpace(q)) != "true" {
		return r, false, "source ownership is not quiescent", nil
	}
	if (r.ScopeUnit != "" || r.ResourceClaim != nil) && !r.Quiescent {
		return r, false, "source resource ownership is unresolved", nil
	}
	finished, err := time.Parse(time.RFC3339Nano, r.FinishedAt)
	if err != nil || r.FinishedAtMS <= 0 || r.FinishedAtMS > legacyImportSafeInteger || finished.UnixMilli() != r.FinishedAtMS {
		return r, false, "", fmt.Errorf("%s: invalid terminal finish time", r.ID)
	}
	return r, true, "", nil
}

func inertLegacyRecord(r Record, jobsDir string) Record {
	r.PID, r.ProcessIdentity, r.ScopeUnit, r.ScopeIdentity, r.ResourceClaim = 0, "", "", nil, nil
	r.ExitPath, r.ResultPath, r.ControlPath, r.GrantPath, r.SpecPath = "", "", "", "", ""
	r.StopRequestedAtMS, r.RequestedStopReason = 0, ""
	r.OwnerPrincipalID, r.OwnerSessionID, r.RequestID, r.ParentActionID = "", "", "", ""
	r.StdoutPath, r.StderrPath = filepath.Join(jobsDir, r.ID+".out"), filepath.Join(jobsDir, r.ID+".err")
	r.LaunchState, r.Quiescent, r.QuiescenceScope = "historical", true, "inert_history"
	r.StoredBytes = 0
	// This does not assert that a legacy PID was killed. There is no Go owner.
	r.QuiescedAt = ""
	return r
}

func replayLegacyImport(ctx context.Context, home string, o LegacyImportOptions) (LegacyImportReport, error) {
	var report LegacyImportReport
	if _, err := os.Lstat(filepath.Join(home, legacyImportMarker)); err == nil {
		return report, errors.New("previous legacy job import is incomplete; use a fresh private destination")
	} else if !os.IsNotExist(err) {
		return report, err
	}
	var b bytes.Buffer
	if _, err := readLegacyFile(ctx, home, legacyImportManifest, legacyImportMetadataLimit, &b); err != nil {
		return report, fmt.Errorf("destination already exists without a completed matching import: %w", err)
	}
	if err := json.Unmarshal(b.Bytes(), &report); err != nil {
		return report, err
	}
	if report.Version != 1 || report.SourceVersion != 1 || report.DestinationVersion != 2 || report.SourceDir != o.SourceDir || report.OriginalJobsDir != o.OriginalJobsDir || report.Destination != home || report.MetadataSHA256 != o.ExpectedMetadataSHA256 || report.SourceRecords != report.ImportedRecords+report.ArchiveOnlyRecords {
		return report, errors.New("destination belongs to a different or invalid legacy import")
	}
	archived, err := readLegacyFile(ctx, home, "legacy-jobs/jobs.json", legacyImportMetadataLimit, io.Discard)
	if err != nil || archived.SHA256 != report.MetadataSHA256 {
		return report, errors.New("completed import's source archive is missing or changed")
	}
	for _, expected := range report.Files {
		limit := o.MaxFileBytes
		if expected.Path == "jobs.json" {
			limit = legacyImportMetadataLimit
		}
		actual, readErr := readLegacyFile(ctx, o.SourceDir, expected.Path, limit, io.Discard)
		if readErr != nil {
			return report, readErr
		}
		if actual.Bytes != expected.Bytes || actual.SHA256 != expected.SHA256 {
			return report, fmt.Errorf("completed import's source snapshot changed: %s", expected.Path)
		}
	}
	f, err := openLegacyFile(home, "jobs.sqlite")
	if err != nil {
		return report, err
	}
	err = f.Close()
	if err != nil {
		return report, err
	}
	report.Replayed = true
	return report, nil
}

// ImportLegacySnapshot imports finished history into a never-used private Home.
// No source file is rewritten, no child/helper is started, and no terminal
// completion is re-emitted. Active/unsettled records are preserved verbatim only
// in legacy-jobs/jobs.json. If publication fails, an explicit marker quarantines
// the destination; New refuses to recover it as if admission had succeeded.
func ImportLegacySnapshot(ctx context.Context, cfg *config.Config, options LegacyImportOptions) (LegacyImportReport, error) {
	var report LegacyImportReport
	o, home, err := normalizeLegacyImport(cfg, options)
	if err != nil {
		return report, err
	}
	var metadata bytes.Buffer
	metadataFile, err := readLegacyFile(ctx, o.SourceDir, "jobs.json", legacyImportMetadataLimit, &metadata)
	if err != nil {
		return report, err
	}
	if metadataFile.SHA256 != o.ExpectedMetadataSHA256 {
		return report, errors.New("private jobs.json does not match the required SHA-256")
	}
	var table legacyTable
	if err = json.Unmarshal(metadata.Bytes(), &table); err != nil {
		return report, err
	}
	if table.Version != 1 || table.Jobs == nil || len(table.Jobs) > o.MaxRecords {
		return report, errors.New("invalid or oversized TypeScript v1 jobs table")
	}
	records := make([]Record, len(table.Jobs))
	eligible := make([]bool, len(table.Jobs))
	seen := map[string]bool{}
	report = LegacyImportReport{Version: 1, SourceVersion: 1, DestinationVersion: 2, SourceDir: o.SourceDir, OriginalJobsDir: o.OriginalJobsDir, Destination: home, MetadataSHA256: metadataFile.SHA256, ImportedAt: time.Now().UTC().Format(time.RFC3339Nano), SourceRecords: len(table.Jobs), Files: []LegacyImportFile{metadataFile}, Jobs: []LegacyImportJob{}, ArchivePath: filepath.Join(home, "legacy-jobs", "jobs.json")}
	for i, raw := range table.Jobs {
		r, ok, reason, decodeErr := decodeLegacyRecord(raw, o.OriginalJobsDir)
		if decodeErr != nil {
			return report, decodeErr
		}
		if seen[r.ID] {
			return report, fmt.Errorf("duplicate source job ID: %s", r.ID)
		}
		seen[r.ID] = true
		records[i], eligible[i] = r, ok
		disposition := "archive_only"
		if ok {
			disposition = "imported_history"
			report.ImportedRecords++
			if r.OutputExpired {
				report.ExpiredRecords++
			}
		} else {
			report.ArchiveOnlyRecords++
		}
		report.Jobs = append(report.Jobs, LegacyImportJob{ID: r.ID, WorkspaceID: r.WorkspaceID, Status: r.Status, Disposition: disposition, Reason: reason, OutputExpired: r.OutputExpired})
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	if err = os.Mkdir(home, 0700); os.IsExist(err) {
		return replayLegacyImport(ctx, home, o)
	} else if err != nil {
		return report, err
	}
	lock, err := lockJobsHome(home, true)
	if err != nil {
		return report, err
	}
	defer lock.Close()
	entries, err := os.ReadDir(home)
	if err != nil {
		return report, err
	}
	if len(entries) != 0 {
		return report, errors.New("fresh job import destination became nonempty before reservation")
	}
	if err = process.ExclusiveWrite(filepath.Join(home, legacyImportMarker), []byte(o.ExpectedMetadataSHA256+"\n"), 0600); err != nil {
		return report, err
	}
	if err = process.SyncDir(filepath.Dir(home)); err != nil {
		return report, err
	}
	jobsDir := filepath.Join(home, "jobs")
	if err = os.Mkdir(jobsDir, 0700); err != nil {
		return report, err
	}
	archiveDir := filepath.Join(home, "legacy-jobs")
	if err = os.Mkdir(archiveDir, 0700); err != nil {
		return report, err
	}
	if err = process.ExclusiveWrite(report.ArchivePath, metadata.Bytes(), 0600); err != nil {
		return report, err
	}
	imported := make([]Record, 0, report.ImportedRecords)
	var total int64
	for i, original := range records {
		if !eligible[i] {
			continue
		}
		r := inertLegacyRecord(original, jobsDir)
		if !r.OutputExpired {
			workspaceHash := sha256.Sum256([]byte(r.WorkspaceID))
			renderedDir := filepath.Join("output", hex.EncodeToString(workspaceHash[:])[:24], r.ID)
			relatives := []string{r.ID + ".out", r.ID + ".err", filepath.Join(renderedDir, "stdout.log"), filepath.Join(renderedDir, "stderr.log")}
			for stream, relative := range relatives {
				limit := min(o.MaxFileBytes, o.MaxBytes-total)
				if limit < 0 {
					return report, errors.New("legacy output total exceeds import bound")
				}
				receipt, copyErr := copyLegacyFile(ctx, o.SourceDir, relative, filepath.Join(jobsDir, relative), limit, stream >= 2)
				if copyErr != nil {
					return report, fmt.Errorf("%s requires its complete retained private output snapshot: %w", r.ID, copyErr)
				}
				total += receipt.Bytes
				r.StoredBytes += receipt.Bytes
				report.Files = append(report.Files, receipt)
				if stream < 2 {
					report.RawBytes += receipt.Bytes
					report.Jobs[i].RawBytes += receipt.Bytes
				} else {
					report.RenderedBytes += receipt.Bytes
					report.Jobs[i].RenderedBytes += receipt.Bytes
				}
				if stream == 0 {
					r.CapturedStdoutBytes = receipt.Bytes
				}
				if stream == 1 {
					r.CapturedStderrBytes = receipt.Bytes
				}
			}
		} else {
			r.StoredBytes = 0
		}
		imported = append(imported, r)
	}
	// Re-read every captured file before committing. A changing private snapshot
	// is a failed import, never a partially acknowledged migration.
	for _, expected := range report.Files {
		limit := o.MaxFileBytes
		if expected.Path == "jobs.json" {
			limit = legacyImportMetadataLimit
		}
		actual, readErr := readLegacyFile(ctx, o.SourceDir, expected.Path, limit, io.Discard)
		if readErr != nil {
			return report, readErr
		}
		if actual.Bytes != expected.Bytes || actual.SHA256 != expected.SHA256 {
			return report, fmt.Errorf("snapshot %s changed before publication", expected.Path)
		}
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	manifest, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return report, err
	}
	manifest = append(manifest, '\n')
	if int64(len(manifest)) > legacyImportMetadataLimit {
		return report, errors.New("legacy import manifest exceeds its bounded receipt limit")
	}
	if err = writeLegacyDatabase(ctx, filepath.Join(home, "jobs.import.sqlite"), imported, manifest); err != nil {
		return report, err
	}
	if err = os.Rename(filepath.Join(home, "jobs.import.sqlite"), filepath.Join(home, "jobs.sqlite")); err != nil {
		return report, err
	}
	if err = process.ExclusiveWrite(filepath.Join(home, legacyImportManifest), manifest, 0600); err != nil {
		return report, err
	}
	if err = syncLegacyDirectories(jobsDir); err != nil {
		return report, err
	}
	if err = process.SyncDir(home); err != nil {
		return report, err
	}
	if err = os.Remove(filepath.Join(home, legacyImportMarker)); err != nil {
		return report, err
	}
	if err = process.SyncDir(home); err != nil {
		return report, err
	}
	return report, nil
}

func syncLegacyDirectories(root string) error {
	var dirs []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := process.SyncDir(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}

func writeLegacyDatabase(ctx context.Context, path string, records []Record, manifest []byte) error {
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite3", u.String()+"?_busy_timeout=5000&_journal_mode=DELETE&_synchronous=FULL&_foreign_keys=on&_txlock=immediate")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, `CREATE TABLE jobs(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL,started_ms INTEGER NOT NULL,status TEXT NOT NULL,origin TEXT NOT NULL,payload BLOB NOT NULL);
CREATE INDEX jobs_workspace ON jobs(workspace_id,started_ms DESC);
CREATE TABLE job_inputs(job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,input_id TEXT NOT NULL REFERENCES jobs(id),PRIMARY KEY(job_id,input_id));
CREATE TABLE job_completions(job_id TEXT PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,payload BLOB NOT NULL);
CREATE TABLE legacy_job_imports(id INTEGER PRIMARY KEY CHECK(id=1),manifest BLOB NOT NULL);
PRAGMA user_version=2;`); err != nil {
		return err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range records {
		payload, e := json.Marshal(r)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO jobs(id,workspace_id,started_ms,status,origin,payload) VALUES(?,?,?,?,?,?)", r.ID, r.WorkspaceID, r.StartedAtMS, r.Status, r.Origin, payload); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO legacy_job_imports(id,manifest) VALUES(1,?)", manifest); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = db.Close(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	if e := f.Close(); err == nil {
		err = e
	}
	return err
}
