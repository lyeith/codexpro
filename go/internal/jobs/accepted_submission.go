package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// queuedSubmissionPayload is shared with ordinary admission so receipt lookup
// uses the original schema, defaults and exact accepted payload fingerprint.
func queuedSubmissionPayload(args map[string]any, commands []map[string]any) (string, int, []any, string, error) {
	key := wire.String(args, "queue_key", "")
	if err := validateQueueKey(key); err != nil {
		return "", 0, nil, "", err
	}
	timeout, err := integerArg(args, "queue_timeout_ms", defaultQueueTimeoutMS, 1000, maxQueueTimeoutMS)
	if err != nil {
		return "", 0, nil, "", err
	}
	payload := []any{commands, wire.String(args, "session_id", ""), timeout}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, nil, "", err
	}
	sum := sha256.Sum256(body)
	return key, timeout, payload, hex.EncodeToString(sum[:]), nil
}

// LookupAcceptedSubmission performs only the existing caller/workspace-bound
// receipt lookup. It cannot create a session, resolve a command CWD, prepare an
// owner, insert a record or dispatch work. A stored key is not new admission.
func (s *Service) LookupAcceptedSubmission(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, bool, error) {
	if name != "queue_jobs" {
		return wire.Result{}, false, nil
	}
	commands, _, err := s.commandRequest(args)
	if err != nil {
		return wire.Result{}, false, err
	}
	key, _, _, digest, err := queuedSubmissionPayload(args, commands)
	if err != nil {
		return wire.Result{}, false, err
	}
	w, err := s.resolver.Resolve(ctx, scope, wire.String(args, "workspace_id", ""))
	if err != nil {
		return wire.Result{}, false, err
	}
	s.admission.Lock()
	records, err := findSubmission(s.Snapshot(), scope.Identity, w.ID, key, digest, len(commands))
	s.admission.Unlock()
	if err != nil || len(records) == 0 {
		return wire.Result{}, false, err
	}
	return s.queuedReceipt(w, key, records, commands), true, nil
}
