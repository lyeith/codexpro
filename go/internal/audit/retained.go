package audit

// retainedAfterAppend uses committed counters while retention limits are unchanged.
// Byte pressure and changed limits retain the full selector's behavior.
func (j *Journal) retainedAfterAppend(added []entry) ([]entry, []entry, operationalLimits) {
	limits := j.currentLimits()
	total := j.retainedBytes
	excess := make(map[string]int)
	for _, e := range added {
		total += e.Length
		excess[e.Bucket]++
	}
	if limits.maxBytes == j.retentionLimits.maxBytes &&
		limits.retainActions == j.retentionLimits.retainActions && total <= limits.maxBytes {
		dropCount := 0
		for bucket, count := range excess {
			count += j.retainedCounts[bucket] - limits.retainActions
			if count > 0 {
				excess[bucket] = count
				dropCount += count
			} else {
				delete(excess, bucket)
			}
		}
		if dropCount == 0 {
			return append(j.entries, added...), nil, limits
		}
		// Untouched buckets already satisfy the unchanged count limit. Drop
		// each affected bucket's oldest excess without modifying committed storage.
		retained := make([]entry, 0, len(j.entries)+len(added)-dropCount)
		dropped := make([]entry, 0, dropCount)
		for _, part := range [2][]entry{j.entries, added} {
			for _, e := range part {
				if excess[e.Bucket] > 0 {
					excess[e.Bucket]--
					dropped = append(dropped, e)
				} else {
					retained = append(retained, e)
				}
			}
		}
		return retained, dropped, limits
	}
	all := append(j.entries, added...)
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
