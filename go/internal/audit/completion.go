package audit

// completionActionID binds a private outbox incarnation to an ordinary opaque
// public action ID. Keeping the identity in that existing field means a crash
// after segment fsync and before index commit does not lose the dedup receipt.
// This domain is part of the durable receipt format and must remain stable.
func completionActionID(key string) string {
	return "cpa_" + digest("codexpro-go/bash-job-completion/v1\x00" + key)[:32]
}

// actionDedupFingerprint is an index key, never an extra public action field.
// Legacy MCP request fingerprints retain their existing semantics. Background
// completions have no public request context, so their durable receipt uses
// their action identity and can be rebuilt from the segment after a crash.
func actionDedupFingerprint(action Action) string {
	if fingerprint, _ := action["request_fingerprint"].(string); fingerprint != "" {
		return fingerprint
	}
	if action["tool_name"] == "bash_job" {
		if id, _ := action["action_id"].(string); id != "" {
			return "completion:" + id
		}
	}
	return ""
}
