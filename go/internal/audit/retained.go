package audit

// retainedAfterAppend avoids rescanning history while the committed counters
// prove both retention limits still hold. Limit changes use the full selector.
func (j *Journal) retainedAfterAppend(added []entry) ([]entry, []entry, operationalLimits) {
	limits := j.currentLimits()
	total := j.retainedBytes
	addedCounts := make(map[string]int)
	needsSelection := limits.maxBytes != j.retentionLimits.maxBytes ||
		limits.retainActions != j.retentionLimits.retainActions
	for _, e := range added {
		total += e.Length
		addedCounts[e.Bucket]++
		if j.retainedCounts[e.Bucket]+addedCounts[e.Bucket] > limits.retainActions {
			needsSelection = true
		}
	}
	all := append(j.entries, added...)
	if !needsSelection && total <= limits.maxBytes {
		return all, nil, limits
	}
	options := j.options
	options.MaxBytes, options.RetainActions = limits.maxBytes, limits.retainActions
	retained, dropped := selectRetained(all, options)
	return retained, dropped, limits
}

// applyRetainedChanges publishes only a committed delta. Completion receipts
// have a separate durable lifetime and are deliberately not removed here.
func (j *Journal) applyRetainedChanges(added, dropped []entry, limits operationalLimits) {
	for _, e := range added {
		j.byID[e.ActionID] = e
		if e.Fingerprint != "" {
			j.byFingerprint[e.Fingerprint] = e
		}
		j.retainedCounts[e.Bucket]++
		j.retainedBytes += e.Length
	}
	for _, e := range dropped {
		delete(j.byID, e.ActionID)
		if existing, ok := j.byFingerprint[e.Fingerprint]; ok && existing.Sequence == e.Sequence {
			delete(j.byFingerprint, e.Fingerprint)
		}
		j.retainedCounts[e.Bucket]--
		if j.retainedCounts[e.Bucket] == 0 {
			delete(j.retainedCounts, e.Bucket)
		}
		j.retainedBytes -= e.Length
	}
	j.retentionLimits = limits
}
