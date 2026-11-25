//go:build linux

package transport

import (
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/plansaccess"
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
	requireMailState := func(stage string) {
		t.Helper()
		receipts, attempts, pending := f.mail.state()
		if got := len(offers); got != 1 {
			t.Fatalf("%s: offers=%d, want exactly the successful promoted call", stage, got)
		}
		if !reflect.DeepEqual(receipts, wantReceipts) || !reflect.DeepEqual(attempts, wantReceipts) ||
			!reflect.DeepEqual(pending, wantPending) {
			t.Fatalf("%s: receipt set differs from the included original: receipts=%v attempts=%v pending=%v",
				stage, receipts, attempts, pending)
		}
	}
	requireMailState("promoted delivery")

	requireDenied := func(stage string) {
		t.Helper()
		denied := &deliveryWriter{header: make(http.Header)}
		_, done := deliveryStart(t, f, denied)
		deliveryAwait(t, done, stage+": restricted MCP call did not finish")
		denied.mu.Lock()
		status := denied.status
		denied.mu.Unlock()
		if status != http.StatusUnauthorized {
			t.Fatalf("%s: restricted existing session returned %d, want 401", stage, status)
		}
		requireMailState(stage)
	}
	p.Credentials[credential].Revoked = true
	saveValidNamedFleetPolicy(t, path, p)
	requireDenied("valid current-credential revocation")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	requireDenied("malformed replacement")

	// An unsupported replacement cannot undo the last validated restriction,
	// even when its record tries to restore the same credential.
	p.Version = 3
	p.Credentials[credential].Revoked = false
	savePolicy(t, path, p)
	requireDenied("unsupported version 3 replacement")
}
