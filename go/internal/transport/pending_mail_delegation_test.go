//go:build linux

package transport

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPendingMailSharedTransportForwardsAttributionWithoutTargetRouting(t *testing.T) {
	f := newPendingHTTPFixture(t, nil)
	// Only Agentmail owns the approved target registrations and summary grants.
	targets := map[string]string{"AP1": "android/ap1", "RDP1": "rdp/rdp1"}
	socket := logicalMailUnixHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		q := r.URL.Query()
		attribution := q.Get("attribution")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/mailboxes/pending-summary" ||
			len(q) != 1 || len(q["attribution"]) != 1 {
			t.Error("shared transport supplied target routing or changed the summary-only route")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+f.tokens[0] ||
			r.Header.Get("X-Agentmail-Expected-Credential") != "home" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, registered := targets[attribution]
		count, sender := 0, ""
		if registered {
			count, sender = f.count["0/"+attribution], f.sender["0/"+attribution]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"count": count, "latest_sender": sender})
	})
	f.server.Config.Set("pendingMailSocket", socket)
	for i, label := range []string{"AP1", "RDP1"} {
		requirePendingLine(t, f.call(t, 0, label, 100+i*10, nil), "")
		f.set(0, label, 3, "plans/coordinator")
		requirePendingLine(t, f.call(t, 0, label, 101+i*10, nil), label+": 3 unread, latest from plans/coordinator")
		f.set(0, label, 0, "")
		requirePendingLine(t, f.call(t, 0, label, 102+i*10, nil), "")
	}
	for i, label := range []string{"AP1", "RDP1"} {
		requirePendingLine(t, f.call(t, 1, label, 130+i, nil), "")
	}
	requirePendingLine(t, f.call(t, 0, "AP3", 140, nil), "")
	requirePendingLine(t, f.call(t, 0, "SP9", 141, nil), "")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 10 {
		t.Fatalf("fresh shared-transport lookups=%d, want10; Agentmail resolves every valid label", f.calls)
	}
}
