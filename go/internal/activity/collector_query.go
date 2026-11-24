package activity

import (
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type CollectorQuery struct {
	Agent          string
	Wait           time.Duration
	SinceCompleted *uint64
	SinceRevision  *uint64
	Epoch          string
}

// ParseCollectorQuery owns the narrow collector query surface for both the
// authenticated transport guard and the handler. No query preserves version 1;
// an explicit agent selects the version 2 completion protocol.
func ParseCollectorQuery(u *url.URL) (CollectorQuery, error) {
	var out CollectorQuery
	bad := errors.New("invalid collector query")
	if u == nil || u.ForceQuery && u.RawQuery == "" {
		return out, bad
	}
	if u.RawQuery == "" {
		return out, nil
	}
	if len(u.RawQuery) > 512 {
		return out, bad
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) == 0 {
		return out, bad
	}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" {
			return out, bad
		}
		switch key {
		case "agent":
			out.Agent = values[0]
			if !wire.ValidAgent(out.Agent) {
				return CollectorQuery{}, bad
			}
		case "wait":
			out.Wait, err = time.ParseDuration(values[0])
			if err != nil || out.Wait < 0 || out.Wait > 30*time.Second {
				return CollectorQuery{}, bad
			}
		case "since_completed", "since_revision":
			for _, char := range values[0] {
				if char < '0' || char > '9' {
					return CollectorQuery{}, bad
				}
			}
			value, err := strconv.ParseUint(values[0], 10, 64)
			if err != nil {
				return CollectorQuery{}, bad
			}
			if key == "since_completed" {
				out.SinceCompleted = &value
			} else {
				out.SinceRevision = &value
			}
		case "epoch":
			if len(values[0]) > 64 {
				return CollectorQuery{}, bad
			}
			for _, char := range values[0] {
				if char < '0' || char > '9' && char < 'A' || char > 'Z' && char < 'a' || char > 'z' {
					return CollectorQuery{}, bad
				}
			}
			out.Epoch = values[0]
		default:
			return CollectorQuery{}, bad
		}
	}
	if out.Agent == "" || out.SinceCompleted != nil && out.SinceRevision != nil ||
		out.Wait > 0 && out.SinceCompleted == nil && out.SinceRevision == nil {
		return CollectorQuery{}, bad
	}
	return out, nil
}

func (q CollectorQuery) unchanged(value CollectorStatus) bool {
	if !value.Activity.Known || len(value.Activity.Agents) != 1 || q.Epoch != "" && q.Epoch != value.Epoch {
		return false
	}
	agent := value.Activity.Agents[0]
	if q.SinceRevision != nil {
		return agent.Revision == *q.SinceRevision
	}
	return q.SinceCompleted != nil && agent.CompletedCalls == *q.SinceCompleted
}
