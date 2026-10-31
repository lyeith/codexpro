package workhistory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type ExportSummary struct {
	Runs    int    `json:"runs"`
	Records int    `json:"records"`
	Bytes   int64  `json:"bytes"`
	SHA256  string `json:"sha256"`
}
type countWriter struct {
	writer io.Writer
	bytes  int64
}

func (w *countWriter) Write(value []byte) (int, error) {
	n, err := w.writer.Write(value)
	w.bytes += int64(n)
	return n, err
}

// Export writes a portable JSONL history for the authenticated principal.
// The immutable source database retains full original records; this public
// projection removes session credentials and operation idempotency secrets.
// It does not create files, mutate the archive or launch historical work.
func (s *Service) Export(ctx context.Context, scope wire.Scope, destination io.Writer) (ExportSummary, error) {
	var summary ExportSummary
	if scope.PrincipalID == "" {
		return summary, wire.Errorf("work_invalid", "Missing authenticated work context.")
	}
	hash := sha256.New()
	count := &countWriter{writer: io.MultiWriter(destination, hash)}
	encoder := json.NewEncoder(count)
	encoder.SetEscapeHTML(false)
	emit := func(kind string, record map[string]any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := encoder.Encode(map[string]any{"kind": kind, "record": wire.RedactValue(record)}); err != nil {
			return err
		}
		summary.Records++
		return nil
	}
	if err := emit("archive", map[string]any{"schema_version": "1.0", "exported_at": s.now(), "read_only": true, "source": "release-0.31 work history", "current_source_observed": s.options.Source != nil}); err != nil {
		return summary, err
	}
	principal := s.principal(scope.PrincipalID)
	for _, run := range s.runs {
		if str(run["principal_id"]) != principal {
			continue
		}
		summary.Runs++
		publicRun := clone(run)
		delete(publicRun, "principal_id")
		if err := emit("run", publicRun); err != nil {
			return summary, err
		}
		for _, table := range []string{"iterations", "operations", "documents", "work_jobs"} {
			order := "rowid"
			if table == "documents" {
				order = "id,revision"
			}
			rows, err := s.db.QueryContext(ctx, "SELECT body FROM "+table+" WHERE run_id=? ORDER BY "+order, run["id"])
			if err != nil {
				return summary, err
			}
			for rows.Next() {
				var body string
				if err = rows.Scan(&body); err != nil {
					rows.Close()
					return summary, err
				}
				var record map[string]any
				if err = json.Unmarshal([]byte(body), &record); err != nil {
					rows.Close()
					return summary, err
				}
				if table == "iterations" {
					record = publicIteration(record)
				}
				if table == "operations" {
					delete(record, "fingerprint")
				}
				if err = emit(table, record); err != nil {
					rows.Close()
					return summary, err
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return summary, err
			}
		}
		rows, err := s.db.QueryContext(ctx, "SELECT sequence,kind,at,body FROM events WHERE run_id=? ORDER BY sequence", run["id"])
		if err != nil {
			return summary, err
		}
		for rows.Next() {
			var sequence int64
			var kind, at, body string
			if err = rows.Scan(&sequence, &kind, &at, &body); err != nil {
				rows.Close()
				return summary, err
			}
			var payload map[string]any
			if err = json.Unmarshal([]byte(body), &payload); err != nil {
				rows.Close()
				return summary, err
			}
			if err = emit("event", map[string]any{"sequence": sequence, "run_id": run["id"], "kind": kind, "at": at, "body": payload}); err != nil {
				rows.Close()
				return summary, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return summary, err
		}
	}
	summary.Bytes = count.bytes
	summary.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return summary, nil
}
