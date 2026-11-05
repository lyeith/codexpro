package transport

import (
	"context"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestConcurrentInitializeReservesOneOwnIdleSlot(t *testing.T) {
	s, _, _, closed := newHTTPFixture(t, nil)
	s.Config.Set("maxHttpSessions", 2)
	actor := wire.Identity{Source: "legacy"}
	for _, id := range []string{"old-idle", "existing-active"} {
		r, state := s.reserveInitialize(t.Context(), actor)
		if state != admissionOK || !s.publishInitialize(r, id, actor) {
			t.Fatalf("could not establish %s", id)
		}
	}
	s.mu.Lock()
	s.sessions["old-idle"].active = 0
	s.sessions["old-idle"].lastSeen = time.Unix(1, 0)
	s.mu.Unlock()

	// Both initializers contend for one replaceable slot. Neither can publish
	// or release capacity until both reservation decisions have returned.
	type attempt struct {
		r     *initReservation
		state admissionState
	}
	start := make(chan struct{})
	results := make(chan attempt, 2)
	for range 2 {
		go func() {
			<-start
			r, state := s.reserveInitialize(t.Context(), actor)
			results <- attempt{r, state}
		}()
	}
	close(start)
	byState := map[admissionState][]*initReservation{}
	for range 2 {
		v := <-results
		byState[v.state] = append(byState[v.state], v.r)
	}
	if len(byState[admissionOK]) != 1 || len(byState[admissionCapacity]) != 1 {
		t.Fatalf("admitted %d, capacity refusals %d; want one each", len(byState[admissionOK]), len(byState[admissionCapacity]))
	}
	if byState[admissionCapacity][0] != nil || closed.Load() != 1 {
		t.Fatal("only the admitted handshake can replace the one idle session")
	}
	winner := byState[admissionOK][0]
	if !s.publishInitialize(winner, "new-active", actor) {
		t.Fatal("could not publish the reserved slot")
	}
	s.releaseInitialize(winner) // HTTP defer after publication must not free an extra slot.
	r, state := s.reserveInitialize(t.Context(), actor)
	if state != admissionCapacity || r != nil || s.SessionCount() != 2 || closed.Load() != 1 {
		t.Fatal("the two active sessions must fill the pool after the reservation converts")
	}
}

func TestInitializeReservationCannotPublishAfterCancellationOrDrain(t *testing.T) {
	s, _, _, closed := newHTTPFixture(t, nil)
	s.Config.Set("maxHttpSessions", 1)
	actor := wire.Identity{Source: "legacy"}
	ctx, cancel := context.WithCancel(t.Context())
	first, state := s.reserveInitialize(ctx, actor)
	if state != admissionOK {
		t.Fatal("expected initial free slot")
	}
	cancel()
	if s.publishInitialize(first, "cancelled", actor) {
		t.Fatal("cancelled handshake published a session")
	}
	s.releaseInitialize(first)
	second, state := s.reserveInitialize(t.Context(), actor)
	if state != admissionOK {
		t.Fatal("cancellation did not release the slot")
	}
	s.releaseInitialize(first) // A late release must leave the new reservation intact.
	if r, got := s.reserveInitialize(t.Context(), actor); got != admissionCapacity || r != nil {
		t.Fatal("a stale release reopened the reserved slot")
	}
	s.BeginDrain()
	if s.publishInitialize(second, "late", actor) {
		t.Fatal("drain allowed a pending handshake to publish")
	}
	s.releaseInitialize(second)
	if r, got := s.reserveInitialize(t.Context(), actor); got != admissionDraining || r != nil {
		t.Fatal("drain admitted a new handshake")
	}
	if s.SessionCount() != 0 || closed.Load() != 0 {
		t.Fatal("unpublished reservations must not create sessions or run owner cleanup")
	}
}

func TestMalformedInitializeReleasesOnlyItsReservation(t *testing.T) {
	s, remote, token, closed := newHTTPFixture(t, nil)
	s.Config.Set("maxHttpSessions", 1)
	// Initialize requires params. The SDK rejects this request before publishing.
	status, header, _ := request(t, remote, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, "", token, nil)
	if status != 400 || header.Get("Mcp-Session-Id") != "" {
		t.Fatalf("malformed initialize returned status %d, session header %q", status, header.Get("Mcp-Session-Id"))
	}
	if s.SessionCount() != 0 || closed.Load() != 0 {
		t.Fatal("a malformed handshake must not create or close a published session")
	}
	for range s.SDK.Sessions() {
		t.Fatal("the SDK retained a rejected handshake")
	}
	initialize(t, remote, token, "2025-11-25")
	if s.SessionCount() != 1 {
		t.Fatal("the next valid initialize could not use the released slot")
	}
}

func TestCloseDiscardsPendingInitialize(t *testing.T) {
	s, _, _, closed := newHTTPFixture(t, nil)
	actor := wire.Identity{Source: "legacy"}
	r, state := s.reserveInitialize(t.Context(), actor)
	if state != admissionOK {
		t.Fatal("expected initial free slot")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	pending := len(s.pending)
	s.mu.Unlock()
	if pending != 0 || s.publishInitialize(r, "after-close", actor) {
		t.Fatal("terminal close must release pending capacity and fence delayed publication")
	}
	s.releaseInitialize(r)
	if s.SessionCount() != 0 || closed.Load() != 0 {
		t.Fatal("an unpublished handshake must not enter the published close path")
	}
}
