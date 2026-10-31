package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func (s *Service) monitor() {
	defer s.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	nextPrune := time.Now()
	for {
		for _, entry := range s.entries() {
			r := copyRecord(*entry.published.Load())
			if entry.launching.Load() {
				continue
			}
			if r.Status == "running" {
				s.observeRunning(r)
			} else if ownershipPending(r) {
				s.enqueueSettlement(r.ID)
			}
		}
		if time.Now().After(nextPrune) {
			_ = s.prune()
			nextPrune = time.Now().Add(time.Second)
		}
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) started(r Record) (process.Started, bool) {
	var started process.Started
	if process.ReadJSON(r.SpecPath+".started", &started, 16384) != nil {
		return started, false
	}
	if r.ScopeUnit != "" && !process.ValidScopeIdentity(started.ScopeIdentity, r.ScopeUnit+".scope") {
		started.ScopeIdentity = nil
	}
	return started, true
}

func (s *Service) observeRunning(r Record) {
	var result process.Result
	if process.ReadJSON(r.ResultPath, &result, 16384) == nil && result.Version == 2 {
		_ = s.finalize(r.ID, &result, "")
		return
	}
	if b, err := os.ReadFile(r.ExitPath); err == nil {
		if code, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && code >= 0 && code <= 255 {
			result.ExitCode = &code
			result.FinishedAtMS = time.Now().UnixMilli()
			_ = s.finalize(r.ID, &result, "")
			return
		}
	}
	started, haveStarted := s.started(r)
	if r.ScopeIdentity == nil && started.ScopeIdentity != nil {
		updated, err := s.update(r.ID, func(next *Record) bool {
			if next.ScopeIdentity != nil {
				return false
			}
			next.ScopeIdentity = started.ScopeIdentity
			return true
		})
		if err == nil {
			r = updated
		}
	}
	state := process.ObserveProcess(r.PID, r.ProcessIdentity)
	alive := state == "active" || state == "unknown"
	if !alive && haveStarted {
		state = process.ObserveProcess(started.PID, started.ProcessIdentity)
		alive = state == "active" || state == "unknown"
	}
	if !alive && time.Now().UnixMilli()-r.StartedAtMS > 750 {
		_ = s.finalize(r.ID, nil, "lost")
		return
	}
	if r.RequestedStopReason == "" && time.Now().UnixMilli() >= r.DeadlineMS {
		_, _ = s.Stop(r.ID, "", "timeout")
		return
	}
	if r.RequestedStopReason != "" {
		if b, err := os.ReadFile(r.ControlPath); err != nil || string(b) != r.RequestedStopReason {
			_ = process.AtomicWrite(r.ControlPath, []byte(r.RequestedStopReason), 0600)
		}
		if time.Now().UnixMilli()-r.StopRequestedAtMS >= 5000 {
			s.enqueueSettlement(r.ID)
		}
	}
}

func (s *Service) finalize(id string, result *process.Result, fallback string) error {
	old, ok := s.Get(id, "")
	if !ok || old.Status != "running" {
		return nil
	}
	stored := s.logBytes(old)
	transition := false
	r, err := s.update(id, func(next *Record) bool {
		if next.Status != "running" {
			return false
		}
		reason := next.RequestedStopReason
		if result != nil {
			next.ExitCode = result.ExitCode
			if result.StopReason != nil {
				reason = *result.StopReason
			}
			if result.Signal != nil {
				next.Signal = *result.Signal
			}
			next.Quiescent = result.Quiescent
			next.QuiescenceScope = "process_group"
			if process.ValidScopeIdentity(result.ScopeIdentity, next.ScopeUnit+".scope") {
				next.ScopeIdentity = result.ScopeIdentity
			}
			next.FinishedAtMS = result.FinishedAtMS
		}
		if reason == "" {
			reason = fallback
		}
		if next.Signal == "" && reason != "" {
			next.Signal = "SIGTERM"
		}
		if next.FinishedAtMS == 0 {
			next.FinishedAtMS = time.Now().UnixMilli()
		}
		next.FinishedAt = wire.Timestamp(time.UnixMilli(next.FinishedAtMS))
		next.StopReason = reason
		if next.ScopeUnit != "" {
			next.Quiescent = false
			next.QuiescenceScope = "systemd_scope"
		}
		if next.ResourceClaim != nil {
			next.Quiescent = false
		}
		if next.PID == 0 && next.ResourceClaim == nil {
			next.Quiescent = true
			next.QuiescenceScope = "not_launched"
		}
		if next.Quiescent {
			next.QuiescedAt = wire.Now()
		}
		switch reason {
		case "timeout":
			next.Status = "timed_out"
		case "stopped":
			next.Status = "stopped"
		case "":
			if next.ExitCode != nil && *next.ExitCode == 0 {
				next.Status = "succeeded"
			} else {
				next.Status = "failed"
			}
		default:
			next.Status = "failed"
		}
		next.CapturedStdoutBytes = fileSize(next.StdoutPath)
		next.CapturedStderrBytes = fileSize(next.StderrPath)
		next.StoredBytes = stored
		transition = true
		return true
	})
	if err != nil {
		return err
	}
	if transition {
		if !ownershipPending(r) {
			_, _ = s.db.Exec("DELETE FROM job_inputs WHERE job_id=?", id)
		} else {
			s.enqueueSettlement(id)
		}
		s.finished(r)
	}
	return nil
}

func (s *Service) enqueueSettlement(id string) {
	e := s.entry(id)
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.settling || time.Now().Before(e.nextSettle) {
		e.mu.Unlock()
		return
	}
	e.settling = true
	e.mu.Unlock()
	select {
	case s.settlements <- id:
	default:
		e.mu.Lock()
		e.settling = false
		e.mu.Unlock()
	}
}

func (s *Service) settlementWorker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case id := <-s.settlements:
			s.settle(id)
			if e := s.entry(id); e != nil {
				e.mu.Lock()
				e.settling = false
				e.nextSettle = time.Now().Add(5 * time.Second)
				e.mu.Unlock()
			}
		}
	}
}

func (s *Service) settle(id string) {
	ctx, cancel := context.WithTimeout(s.ctx, 12*time.Second)
	defer cancel()
	r, ok := s.Get(id, "")
	if !ok {
		return
	}
	started, _ := s.started(r)
	identity := r.ScopeIdentity
	if identity == nil {
		identity = started.ScopeIdentity
	}
	if r.Status == "running" {
		if r.RequestedStopReason == "" || time.Now().UnixMilli()-r.StopRequestedAtMS < 5000 {
			return
		}
		if r.ScopeUnit != "" {
			if identity != nil && process.SignalScope(ctx, identity, syscall.SIGKILL) {
				_ = s.finalize(id, nil, r.RequestedStopReason)
			}
		} else if started.CommandPID > 0 {
			_ = process.SignalGroup(started.CommandPID, syscall.SIGKILL)
		}
		return
	}
	if !ownershipPending(r) {
		return
	}
	quiet := r.Quiescent
	if r.ScopeUnit != "" {
		quiet = identity != nil && process.SettleScope(ctx, identity)
		if identity == nil && r.PID == 0 {
			if _, err := os.Stat(r.GrantPath); os.IsNotExist(err) {
				quiet = true
			}
		}
	}
	if r.ResourceClaim != nil {
		code := 125
		if r.Status == "succeeded" {
			code = 0
		} else if r.ExitCode != nil && *r.ExitCode != 0 {
			code = *r.ExitCode
		}
		quiet = s.settleResourcesContext(ctx, r, code) == nil
	}
	if !quiet {
		return
	}
	_, err := s.update(id, func(next *Record) bool {
		if next.Quiescent {
			return false
		}
		next.Quiescent = true
		next.QuiescedAt = wire.Now()
		if identity != nil {
			next.ScopeIdentity = identity
			next.QuiescenceScope = "systemd_scope"
		}
		return true
	})
	if err == nil {
		_, _ = s.db.Exec("DELETE FROM job_inputs WHERE job_id=?", id)
	}
}

func (s *Service) wait(ctx context.Context, ids []string, mode string, timeout time.Duration, interruptible bool) (bool, error) {
	deadline := time.NewTimer(max(0, timeout))
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		changed := s.changeSignal()
		finished := 0
		for _, id := range ids {
			r, err := s.require(id, "")
			if err != nil {
				return false, err
			}
			if r.Status != "running" {
				finished++
			}
		}
		if finished == len(ids) || (mode == "any" && finished > 0) {
			return false, nil
		}
		if s.closed.Load() {
			return interruptible, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-s.ctx.Done():
			return interruptible, nil
		case <-deadline.C:
			return false, nil
		case <-changed:
		case <-ticker.C:
		}
	}
}

func (s *Service) waitOutput(ctx context.Context, r Record, cursor string, timeout time.Duration) (bool, error) {
	deadline := time.NewTimer(max(0, timeout))
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		changed := s.changeSignal()
		r, err := s.require(r.ID, r.WorkspaceID)
		if err != nil {
			return false, err
		}
		page, err := s.page(r, cursor, 1)
		if err != nil {
			return false, err
		}
		if page.HasMore || page.ReturnedBytes > 0 || r.Status != "running" {
			return false, nil
		}
		if s.closed.Load() {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-s.ctx.Done():
			return true, nil
		case <-deadline.C:
			return false, nil
		case <-changed:
		case <-ticker.C:
		}
	}
}

func (s *Service) logBytes(r Record) int64 {
	if r.StoredBytes > 0 && r.Status != "running" {
		return r.StoredBytes
	}
	dir := s.outputDir(r)
	return fileSize(r.StdoutPath) + fileSize(r.StderrPath) + fileSize(filepath.Join(dir, "stdout.log")) + fileSize(filepath.Join(dir, "stderr.log"))
}

func (s *Service) prune() error {
	if err := s.releaseSettledPins(); err != nil {
		return err
	}
	s.admission.Lock()
	all := s.Snapshot()
	protected := map[string]bool{}
	finished := []Record{}
	for _, r := range all {
		if r.Status == "running" || ownershipPending(r) {
			for _, id := range r.InputJobIDs {
				protected[id] = true
			}
		}
		if r.Status != "running" {
			finished = append(finished, r)
		}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].FinishedAtMS > finished[j].FinishedAtMS })
	var pinned int64
	for _, r := range finished {
		if protected[r.ID] && !r.OutputExpired {
			pinned += s.logBytes(r)
		}
	}
	budget := max(int64(0), s.cfg.Int64("maxRetainedJobBytes", 536870912)-pinned)
	counts := map[string]int{}
	used := map[string]int64{}
	expire := []string{}
	now := time.Now().UnixMilli()
	for _, r := range finished {
		if ownershipPending(r) || r.OutputExpired {
			continue
		}
		category := "background"
		if r.Origin == "foreground" {
			category = "foreground"
		}
		key := r.WorkspaceID + ":" + category
		counts[key]++
		if protected[r.ID] {
			continue
		}
		bytes := s.logBytes(r)
		used[category] += bytes
		cap := budget * 3 / 4
		history := s.cfg.Int("maxJobHistoryPerWorkspace", 50)
		if category == "foreground" {
			cap = budget / 4
			history = min(20, history)
		}
		if now-r.FinishedAtMS <= s.cfg.Int64("jobRetentionMs", 86400000) && counts[key] <= history && used[category] <= cap {
			continue
		}
		expire = append(expire, r.ID)
		used[category] -= bytes
	}
	if len(expire) == 0 {
		s.admission.Unlock()
		return s.pruneTombstones()
	}
	sort.Strings(expire)
	entries := make([]*jobEntry, 0, len(expire))
	next := make([]Record, 0, len(expire))
	for _, id := range expire {
		e := s.entry(id)
		if e == nil {
			continue
		}
		e.mu.Lock()
		r := copyRecord(e.record)
		if ownershipPending(r) || r.Status == "running" || r.OutputExpired {
			e.mu.Unlock()
			continue
		}
		r.OutputExpired = true
		r.CapturedStdoutBytes = fileSize(r.StdoutPath)
		r.CapturedStderrBytes = fileSize(r.StderrPath)
		entries = append(entries, e)
		next = append(next, r)
	}
	tx, err := s.db.Begin()
	if err == nil {
		for _, r := range next {
			var b []byte
			b, err = json.Marshal(r)
			if err != nil {
				break
			}
			_, err = tx.Exec("UPDATE jobs SET payload=? WHERE id=?", b, r.ID)
			if err != nil {
				break
			}
		}
		if err == nil {
			err = tx.Commit()
		}
		_ = tx.Rollback()
	}
	if err == nil {
		for i, e := range entries {
			publishEntry(e, next[i])
		}
	}
	for _, e := range entries {
		e.mu.Unlock()
	}
	s.admission.Unlock()
	if err != nil {
		return err
	}
	for _, r := range next {
		s.removeOutput(r)
	}
	s.notify()
	return s.pruneTombstones()
}

func (s *Service) pruneTombstones() error {
	s.admission.Lock()
	defer s.admission.Unlock()
	finished := []Record{}
	protected := map[string]bool{}
	for _, r := range s.Snapshot() {
		if r.Status == "running" || ownershipPending(r) {
			for _, id := range r.InputJobIDs {
				protected[id] = true
			}
		}
		if r.OutputExpired && !ownershipPending(r) {
			finished = append(finished, r)
		}
	}
	if len(finished) <= 500 {
		return nil
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].FinishedAtMS > finished[j].FinishedAtMS })
	for _, r := range finished[500:] {
		if protected[r.ID] {
			continue
		}
		var pending int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM job_completions WHERE job_id=?", r.ID).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			continue
		}
		if _, err := s.db.Exec("DELETE FROM job_inputs WHERE job_id=?", r.ID); err != nil {
			return err
		}
		if _, err := s.db.Exec("DELETE FROM jobs WHERE id=?", r.ID); err != nil {
			return err
		}
		s.mu.Lock()
		delete(s.records, r.ID)
		s.mu.Unlock()
	}
	return nil
}

func (s *Service) removeOutput(r Record) {
	if entry := s.entry(r.ID); entry != nil {
		entry.outputMu.Lock()
		defer entry.outputMu.Unlock()
	}
	for _, path := range []string{r.StdoutPath, r.StderrPath, r.ExitPath, r.ResultPath, r.ControlPath, r.GrantPath, r.SpecPath, r.SpecPath + ".started"} {
		if process.Within(s.dir, path) {
			_ = os.Remove(path)
		}
	}
	if dir := s.outputDir(r); process.Within(s.dir, dir) {
		_ = os.RemoveAll(dir)
	}
}

func (s *Service) releaseSettledPins() error {
	rows, err := s.db.Query("SELECT DISTINCT job_id FROM job_inputs")
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		if r, ok := s.Get(id, ""); ok && r.Status != "running" && !ownershipPending(r) {
			ids = append(ids, id)
		}
	}
	if rowErr := rows.Err(); err == nil {
		err = rowErr
	}
	rows.Close()
	if err != nil || len(ids) == 0 {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err = tx.Exec("DELETE FROM job_inputs WHERE job_id=?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
