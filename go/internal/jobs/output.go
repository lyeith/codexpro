package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const outputPageDefault = 16384
const outputPageMax = 24576
const outputResponseMax = 192 * 1024

type Output struct {
	Stdout      string
	Stderr      string
	StdoutBytes int64
	StderrBytes int64
	Truncated   bool
}

type outputPage struct {
	Stdout         string
	Stderr         string
	ReturnedBytes  int
	NextCursor     string
	HasMore        bool
	OutputComplete bool
}

func (p outputPage) fields() map[string]any {
	return map[string]any{"stdout": p.Stdout, "stderr": p.Stderr, "returned_bytes": p.ReturnedBytes, "next_cursor": p.NextCursor, "has_more": p.HasMore, "output_complete": p.OutputComplete, "output_mode": "incremental"}
}

func fileSize(path string) int64 {
	info, e := os.Stat(path)
	if e != nil {
		return 0
	}
	return info.Size()
}
func (s *Service) workspaceOutputDir(workspace string) string {
	sum := sha256.Sum256([]byte(workspace))
	return filepath.Join(s.dir, "output", hex.EncodeToString(sum[:])[:24])
}
func (s *Service) outputDir(r Record) string {
	return filepath.Join(s.workspaceOutputDir(r.WorkspaceID), r.ID)
}

func (s *Service) OutputMetadata(r Record) map[string]any {
	if entry := s.entry(r.ID); entry != nil {
		entry.outputMu.RLock()
		defer entry.outputMu.RUnlock()
		if fresh, ok := s.Get(r.ID, r.WorkspaceID); ok {
			r = fresh
		}
	}
	return s.outputMetadata(r)
}

func (s *Service) outputMetadata(r Record) map[string]any {
	available := !r.OutputExpired
	stdout, stderr := r.CapturedStdoutBytes, r.CapturedStderrBytes
	var outAvailable, errAvailable int64
	if available {
		stdout = fileSize(r.StdoutPath)
		stderr = fileSize(r.StderrPath)
		dir := s.outputDir(r)
		outAvailable = fileSize(filepath.Join(dir, "stdout.log"))
		errAvailable = fileSize(filepath.Join(dir, "stderr.log"))
	}
	var expires any
	if r.FinishedAtMS != 0 {
		expires = wire.Timestamp(time.UnixMilli(r.FinishedAtMS + s.cfg.Int64("jobRetentionMs", 86400000)))
	}
	m := map[string]any{"stdout_bytes": stdout, "stderr_bytes": stderr, "available_stdout_bytes": outAvailable, "available_stderr_bytes": errAvailable, "output_available": available, "output_growing": r.Status == "running", "output_expires_at": expires, "retention_note": "May expire earlier under bounded storage/count retention; input_job_ids pins logs during analysis."}
	if available {
		m["output_files"] = map[string]any{"stdout": "$CODEXPRO_JOB_OUTPUT_DIR/" + r.ID + "/stdout.log", "stderr": "$CODEXPRO_JOB_OUTPUT_DIR/" + r.ID + "/stderr.log"}
	}
	return m
}

type ranged struct {
	text  string
	next  int64
	bytes int64
}

func readRange(path string, offset int64, budget int, tail bool) (ranged, error) {
	length := fileSize(path)
	start := offset
	if tail {
		start = max(int64(0), length-int64(budget))
	}
	count := max(int64(0), min(int64(budget), length-start))
	if count == 0 {
		return ranged{next: start, bytes: length}, nil
	}
	f, e := os.Open(path)
	if e != nil {
		return ranged{}, e
	}
	defer f.Close()
	b := make([]byte, int(count))
	n, e := f.ReadAt(b, start)
	if e != nil && e != io.EOF {
		return ranged{}, e
	}
	b = b[:n]
	first, last := 0, len(b)
	if tail {
		for first < last && b[first]&0xc0 == 0x80 {
			first++
		}
	}
	for last > first && !utf8.Valid(b[first:last]) {
		if len(b)-last >= 3 {
			return ranged{}, wire.Errorf("job_output_invalid", "Invalid rendered output")
		}
		last--
	}
	return ranged{text: string(b[first:last]), next: start + int64(last), bytes: length}, nil
}

func (s *Service) read(r Record, budget int, head bool) (Output, error) {
	if entry := s.entry(r.ID); entry != nil {
		entry.outputMu.RLock()
		defer entry.outputMu.RUnlock()
		if fresh, ok := s.Get(r.ID, r.WorkspaceID); ok {
			r = fresh
		}
	}
	meta := s.outputMetadata(r)
	stdout := meta["stdout_bytes"].(int64)
	stderr := meta["stderr_bytes"].(int64)
	if r.OutputExpired {
		return Output{StdoutBytes: stdout, StderrBytes: stderr, Truncated: true}, nil
	}
	dir := s.outputDir(r)
	out, e := readRange(filepath.Join(dir, "stdout.log"), 0, max(0, budget), !head)
	if e != nil {
		return Output{}, e
	}
	err, e := readRange(filepath.Join(dir, "stderr.log"), 0, max(0, budget), !head)
	if e != nil {
		return Output{}, e
	}
	return Output{Stdout: out.text, Stderr: err.text, StdoutBytes: stdout, StderrBytes: stderr, Truncated: out.bytes > int64(len(out.text)) || err.bytes > int64(len(err.text))}, nil
}

func (s *Service) ReadTail(ctx context.Context, id, workspace string, maxBytes int) (Output, error) {
	if e := ctx.Err(); e != nil {
		return Output{}, e
	}
	r, e := s.require(id, workspace)
	if e != nil {
		return Output{}, e
	}
	return s.read(r, min(max(0, maxBytes), 65536), false)
}

func parseSafeInteger(n json.Number) (int64, bool) {
	f, e := strconv.ParseFloat(string(n), 64)
	if e != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 9007199254740991 || math.Floor(f) != f {
		return 0, false
	}
	return int64(f), true
}

func (s *Service) page(r Record, cursor string, budget int) (outputPage, error) {
	if entry := s.entry(r.ID); entry != nil {
		entry.outputMu.RLock()
		defer entry.outputMu.RUnlock()
		if fresh, ok := s.Get(r.ID, r.WorkspaceID); ok {
			r = fresh
		}
	}
	if r.OutputExpired {
		return outputPage{}, jobError("job_output_expired", "Job output expired under retention. Rerun only if needed and safe.")
	}
	identity := r.WorkspaceID + ":" + r.ID + ":" + r.StartedAt
	offsets := [2]int64{}
	invalid := func() (outputPage, error) {
		return outputPage{}, jobError("job_cursor_invalid", "Cursor does not belong to this workspace/job output.")
	}
	if cursor != "" {
		if len(cursor) > 2048 {
			return invalid()
		}
		b, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil {
			b, e = base64.URLEncoding.DecodeString(cursor)
		}
		if e != nil {
			return invalid()
		}
		var value struct {
			V       json.Number   `json:"v"`
			ID      string        `json:"id"`
			Offsets []json.Number `json:"offsets"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(b)))
		decoder.UseNumber()
		if e = decoder.Decode(&value); e != nil || value.ID != identity || len(value.Offsets) != 2 {
			return invalid()
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return invalid()
		}
		version, ok := parseSafeInteger(value.V)
		if !ok || version != 1 {
			return invalid()
		}
		for i, n := range value.Offsets {
			offset, ok := parseSafeInteger(n)
			if !ok {
				return invalid()
			}
			offsets[i] = offset
		}
	}
	dir := s.outputDir(r)
	files := [2]string{filepath.Join(dir, "stdout.log"), filepath.Join(dir, "stderr.log")}
	lengths := [2]int64{fileSize(files[0]), fileSize(files[1])}
	for i, offset := range offsets {
		if offset > lengths[i] {
			return outputPage{}, jobError("job_cursor_invalid", "Output changed or cursor is beyond retained data.")
		}
		if offset == 0 || offset == lengths[i] {
			continue
		}
		f, e := os.Open(files[i])
		if e != nil {
			return outputPage{}, e
		}
		b := []byte{0}
		_, e = f.ReadAt(b, offset)
		_ = f.Close()
		if e != nil {
			return outputPage{}, e
		}
		if b[0]&0xc0 == 0x80 {
			return outputPage{}, jobError("job_cursor_invalid", "Cursor splits a UTF-8 character.")
		}
	}
	budget = max(0, budget)
	outBudget := budget - int(min(lengths[1]-offsets[1], int64(budget/2)))
	out, e := readRange(files[0], offsets[0], outBudget, false)
	if e != nil {
		return outputPage{}, e
	}
	err, e := readRange(files[1], offsets[1], budget-len(out.text), false)
	if e != nil {
		return outputPage{}, e
	}
	next := [2]int64{out.next, err.next}
	hasMore := next[0] < lengths[0] || next[1] < lengths[1]
	b, e := json.Marshal(struct {
		V       int      `json:"v"`
		ID      string   `json:"id"`
		Offsets [2]int64 `json:"offsets"`
	}{1, identity, next})
	if e != nil {
		return outputPage{}, e
	}
	return outputPage{Stdout: out.text, Stderr: err.text, ReturnedBytes: len(out.text) + len(err.text), NextCursor: base64.RawURLEncoding.EncodeToString(b), HasMore: hasMore, OutputComplete: r.Status != "running" && !hasMore}, nil
}
