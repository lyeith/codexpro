//go:build linux

package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestNamedFleetPromotionPreservesLogicalMailDelivery(t *testing.T) {
	// Each original fits alone; their combined decoded bodies exceed the whole
	// text budget, exercising the batch guard through an authenticated MCP call.
	bodyBytes := deliveryTextBudget/2 + 1
	f := newDeliveryFixture(t, strings.Repeat("a", bodyBytes), strings.Repeat("b", bodyBytes))
	path := f.server.Config.String("principalsFile", "")
	p, err := plansaccess.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	before, _, authenticated := f.server.authenticate(f.token)
	agent, registered := p.Agent(before.AgentID)
	if !authenticated || !registered || before.Source != "named" || before.PrincipalType != "system" ||
		p.Version != 1 || agent.ID != "crew-alpha" || agent.Kind != "leaf" ||
		agent.Address != deliveryMailboxAddress || agent.FleetCoordinator {
		t.Fatal("delivery fixture is not an active ordinary named system leaf before promotion")
	}
	credential := -1
	for i, record := range p.Credentials {
		if record.ID == before.CredentialID {
			credential = i
			break
		}
	}
	if credential < 0 || p.Credentials[credential].Format != "" || p.Credentials[credential].Audience != "both" {
		t.Fatal("delivery fixture does not use an ordinary credential shared by MCP and Agentmail")
	}

	// newDeliveryFixture already initialized this session under version 1.
	// Promote its canonical registration without changing the session, token,
	// logical-mail configuration, credential pin or original mailbox.
	if err := p.GrantFleetCoordinator(agent.Address); err != nil {
		t.Fatal(err)
	}
	promoted, _ := p.Agent(agent.ID)
	if p.Version != 2 || !promoted.FleetCoordinator || promoted.ProjectID != "system" {
		t.Fatal("ordinary named promotion did not establish the version 2 project capability")
	}
	saveValidNamedFleetPolicy(t, path, p)
	if after, _, ok := f.server.authenticate(f.token); !ok || after != before {
		t.Fatal("promotion changed the existing MCP credential binding")
	}

	offers := make(chan struct{}, 4)
	w := &deliveryWriter{header: make(http.Header), writeGate: newDeliveryGate(t), flushGate: newDeliveryGate(t)}
	f.mail.mu.Lock()
	f.mail.offerEntered = offers
	f.mail.beforeReceipt = w.receiptAllowed
	f.mail.mu.Unlock()
	_, done := deliveryStart(t, f, w)
	deliveryAwait(t, w.writeGate.entered, "promoted session never reached its complete result write")
	if _, attempts, _ := f.mail.state(); len(attempts) != 0 {
		t.Fatal("promoted session receipted mail before the complete result write")
	}
	w.writeGate.open()
	deliveryAwait(t, w.flushGate.entered, "promoted session never reached its final result flush")
	if _, attempts, _ := f.mail.state(); len(attempts) != 0 {
		t.Fatal("promoted session receipted mail before the successful result flush")
	}
	w.flushGate.open()
	deliveryAwait(t, done, "promoted session did not finish its combined response")

	data, _, _ := w.snapshot()
	result, complete := deliveryResult(data, w.id)
	if !complete {
		t.Fatal("promoted session did not receive a whole SDK result")
	}
	payload, _ := deliveryPayload(t, result, f.ordinary)
	deliveryRequireOriginals(t, payload, f.mail.originals[:1]...)
	if payload["more"] != true {
		t.Fatal("promoted session omitted the over-budget remainder notice")
	}
	wantReceipts := deliveryIDs(f.mail.originals[:1])
	wantPending := deliveryIDs(f.mail.originals[1:])
	wantOriginals := append([]deliveryOriginal(nil), f.mail.originals...)
	requireMailState := func(t *testing.T, stage string) {
		t.Helper()
		receipts, attempts, pending := f.mail.state()
		f.mail.mu.Lock()
		originals := append([]deliveryOriginal(nil), f.mail.originals...)
		f.mail.mu.Unlock()
		if !reflect.DeepEqual(originals, wantOriginals) {
			t.Fatalf("%s: accepted or pending original contents changed", stage)
		}
		if got := len(offers); got != 1 {
			t.Fatalf("%s: offers=%d, want exactly the successful promoted call", stage, got)
		}
		if !reflect.DeepEqual(receipts, wantReceipts) || !reflect.DeepEqual(attempts, wantReceipts) ||
			!reflect.DeepEqual(pending, wantPending) {
			t.Fatalf("%s: receipt set differs from the included original: receipts=%v attempts=%v pending=%v",
				stage, receipts, attempts, pending)
		}
	}
	requireMailState(t, "promoted delivery")

	requireDenied := func(t *testing.T, stage string, consumer *deliveryFixture) {
		t.Helper()
		denied := &deliveryWriter{header: make(http.Header)}
		_, done := deliveryStart(t, consumer, denied)
		deliveryAwait(t, done, stage+": restricted MCP call did not finish")
		denied.mu.Lock()
		status := denied.status
		denied.mu.Unlock()
		if status != http.StatusUnauthorized {
			t.Fatalf("%s: restricted existing session returned %d, want 401", stage, status)
		}
		requireMailState(t, stage)
	}
	p.Credentials[credential].Revoked = true
	saveValidNamedFleetPolicy(t, path, p)
	validRevoked, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	requireDenied(t, "valid current-credential revocation", f)
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	requireDenied(t, "malformed replacement", f)

	// An unsupported replacement cannot undo the last validated restriction,
	// even when its record tries to restore the same credential.
	p.Version = 3
	p.Credentials[credential].Revoked = false
	savePolicy(t, path, p)
	requireDenied(t, "unsupported version 3 replacement", f)

	t.Run("cold", func(t *testing.T) {
		// Restore the exact valid revoked policy, and replace only the
		// consumer. The original mailbox, socket, offers and receipts stay.
		if err := os.WriteFile(path, validRevoked, 0600); err != nil {
			t.Fatal(err)
		}
		coldServer, coldRemote, _, _ := newHTTPFixture(t, func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
			return f.ordinary, nil
		})
		for _, key := range []string{"principalsFile", "logicalMailSocket", "logicalMailPrincipal", "logicalMailAgent", "logicalMailMailbox"} {
			coldServer.Config.Set(key, f.server.Config.String(key, ""))
		}
		if coldServer.policyReader == f.server.policyReader {
			t.Fatal("cold mail consumer reused the warm policy reader")
		}
		cold := &deliveryFixture{
			server: coldServer, remote: coldRemote, token: f.token, session: f.session,
			mail: f.mail, ordinary: f.ordinary,
		}
		requireDenied(t, "cold consumer with restored valid revocation", cold)

		// A different valid actor can use a fresh MCP session, but cannot
		// inherit the revoked actor's session or logical-mail identity.
		otherSession := initialize(t, coldRemote, f.otherToken, "2025-11-25")
		other := &deliveryFixture{
			server: coldServer, remote: coldRemote, token: f.otherToken, session: otherSession,
			mail: f.mail, ordinary: f.ordinary,
		}
		result, err := other.remoteCall(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(f.ordinary)
		if err != nil {
			t.Fatal(err)
		}
		var ordinary map[string]any
		if err := json.Unmarshal(encoded, &ordinary); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(result, ordinary) {
			t.Fatal("unrelated cold actor inherited logical mail or changed its ordinary result")
		}
		const list = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
		if status, _, _ := request(t, coldRemote, "POST", "/mcp", list, f.session, f.otherToken, nil); status != http.StatusNotFound {
			t.Fatalf("cold consumer adopted the revoked actor's old session: %d", status)
		}
		if status, _, _ := request(t, f.remote, "POST", "/mcp", list, f.session, f.otherToken, nil); status != http.StatusForbidden {
			t.Fatalf("unrelated actor crossed the retained warm session binding: %d", status)
		}
		requireMailState(t, "cold revoked and unrelated consumer calls")
	})
}
