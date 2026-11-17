//go:build linux

package transport

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPendingMailSharedTransportForwardsExactApprovedAttribution(t *testing.T) {
	f := newPendingHTTPFixture(t, nil)
	// Both labels use the same authenticated source. Neither target registration
	// lends a credential to the connector; the backend grants only this summary.
	f.server.Config.Set("pendingMailBindings", `[{"label":"AP1","principal":"alpha","agent":"crew-alpha","mailbox":"android/ap1","logical_recipient":"AP1"},{"label":"RDP1","principal":"alpha","agent":"crew-alpha","mailbox":"rdp/rdp1","logical_recipient":"RDP1"}]`)
	targets := map[string]string{"AP1": "android/ap1", "RDP1": "rdp/rdp1"}
	socket := logicalMailUnixHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		q := r.URL.Query()
		attribution := q.Get("attribution")
		address, configured := targets[attribution]
		if r.Method != http.MethodGet || r.URL.Path != "/v1/mailboxes/pending-summary" ||
			len(q) != 3 || len(q["address"]) != 1 || len(q["logical_recipient"]) != 1 || len(q["attribution"]) != 1 ||
			!configured || q.Get("address") != address || q.Get("logical_recipient") != attribution ||
			r.Header.Get("Authorization") != "Bearer "+f.tokens[0] || r.Header.Get("X-Agentmail-Expected-Credential") != "home" {
			t.Error("shared transport changed the exact target, logical selection, label, bearer or registration pin")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"address": address, "logical_recipient": attribution,
			"count": f.count[address], "latest_sender": f.sender[address],
		})
	})
	f.server.Config.Set("pendingMailSocket", socket)
	for i, label := range []string{"AP1", "RDP1"} {
		address := targets[label]
		requirePendingLine(t, f.call(t, 0, label, 100+i*10, nil), "")
		f.set(address, 3, "plans/coordinator")
		requirePendingLine(t, f.call(t, 0, label, 101+i*10, nil), label+": 3 unread, latest from plans/coordinator")
		// No sleep/cache expiry. The API test performs the actual owner pull;
		// this boundary must revalidate its resulting zero on the next call.
		f.set(address, 0, "")
		requirePendingLine(t, f.call(t, 0, label, 102+i*10, nil), "")
	}
	for i, label := range []string{"AP1", "RDP1"} {
		requirePendingLine(t, f.call(t, 1, label, 130+i, nil), "")
	}
	requirePendingLine(t, f.call(t, 0, "AP3", 140, nil), "")
	requirePendingLine(t, f.call(t, 0, "SP9", 141, nil), "")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 6 {
		t.Fatalf("fresh exact shared-transport lookups=%d, want 6; unbound labels/callers must not query", f.calls)
	}
}
