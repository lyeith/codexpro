package jobs

import (
	"encoding/json"
	"errors"
	"strings"
)

// The existing started/result readers each accept at most 16KiB. Six bytes per
// source byte cover JSON string escaping on re-encoding; 4KiB covers generated
// fixed-width IDs, scalar values and lifecycle keys. This is a storage reserve,
// not a command-size limit. Configured helper bytes are counted separately.
const queuedLifecyclePayloadBytes int64 = 2*16384*6 + 4096

func (s *Service) pendingPayloadBytes(bytes int64) int64 {
	return s.pendingPayloadWithClaim(bytes, 0)
}

func (s *Service) pendingPayloadWithClaim(bytes, claimGrowth int64) int64 {
	helper, _ := json.Marshal(s.cfg.HostResourcesHelper)
	// Both the live row and its required immutable completion snapshot can grow.
	return 2 * (bytes + queuedLifecyclePayloadBytes + int64(len(helper)) + claimGrowth)
}

// Guarded preparation duplicates the immutable offered context in claim v2.
// Count that exact future JSON growth while no claim exists; after preparation
// SQLite already includes it. Dummy IDs have the same fixed width, no authority.
func guardedClaimGrowth(r Record) (int64, error) {
	if r.GuardedResourceOffer == nil || r.ResourceClaim != nil {
		return 0, nil
	}
	claim, err := r.GuardedResourceOffer.Claim(strings.Repeat("0", 32), strings.Repeat("0", 32))
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(claim)
	if err != nil {
		return 0, err
	}
	return int64(len(body) + len(`,"resource_claim":`)), nil
}

type queuedPayloadProjection struct {
	pending     bool
	claimGrowth int64
}

// submissionPayloadBytes inventories actual encoded SQLite bytes. Unfinished or
// unsettled keyed jobs also reserve their required future completion copy and
// bounded lifecycle growth. Terminal settled receipts count actual jobs+outbox
// bytes, releasing only unused reservation. No second ledger is maintained.
// Callers hold the existing admission mutex.
func (s *Service) submissionPayloadBytes() (int64, error) {
	// Keep every keyed record, including settled rows with no future reservation.
	selected := map[string]queuedPayloadProjection{}
	for _, entry := range s.entries() {
		r := entry.published.Load()
		if r != nil && r.SubmissionKey != "" {
			projection := queuedPayloadProjection{pending: unfinished(*r) || ownershipPending(*r)}
			if projection.pending {
				var err error
				projection.claimGrowth, err = guardedClaimGrowth(*r)
				if err != nil {
					return 0, err
				}
			}
			selected[r.ID] = projection
		}
	}
	if len(selected) == 0 {
		return 0, nil
	}
	rows, err := s.db.Query(`SELECT jobs.id, length(CAST(jobs.payload AS BLOB)),
 COALESCE(length(CAST(job_completions.payload AS BLOB)),0)
 FROM jobs LEFT JOIN job_completions ON job_completions.job_id=jobs.id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var total int64
	for rows.Next() {
		var id string
		var bytes, completion int64
		if err = rows.Scan(&id, &bytes, &completion); err != nil {
			return 0, err
		}
		projection, present := selected[id]
		if !present {
			continue
		}
		if bytes <= 0 {
			return 0, errors.New("queued submission payload is unavailable")
		}
		reserved := bytes + completion
		if projection.pending {
			reserved = max(reserved, s.pendingPayloadWithClaim(bytes, projection.claimGrowth))
		}
		total += reserved
		delete(selected, id)
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if len(selected) != 0 {
		return 0, errors.New("queued submission payload inventory is incomplete")
	}
	return total, nil
}

func (s *Service) recordPayloadBytes(records []Record) (int64, error) {
	var total int64
	for _, r := range records {
		body, err := json.Marshal(r)
		if err != nil {
			return 0, err
		}
		growth, err := guardedClaimGrowth(r)
		if err != nil {
			return 0, err
		}
		total += s.pendingPayloadWithClaim(int64(len(body)), growth)
	}
	return total, nil
}
