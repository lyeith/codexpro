//go:build linux

package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const deliveryMailboxAddress = "system/alpha"
const deliveryLogicalRecipient = "SP6"
const deliveryTextBudget = 16 << 10

type deliveryOriginal struct {
	id   string
	seq  int64
	body string
}

func (m deliveryOriginal) envelope() map[string]any {
	return map[string]any{
		"id": m.id, "seq": m.seq, "from": "fixture/coordinator",
		"to": deliveryMailboxAddress, "logical_sender": "SRC",
		"logical_recipient": deliveryLogicalRecipient, "body": m.body,
		"reply_to": "original-question", "created_at": "2026-11-09T00:00:00Z",
		"delivery_mode": "pull", "delivery_status": "awaiting_pull",
	}
}

type deliveryMailbox struct {
	t             *testing.T
	mu            sync.Mutex
	token         string
	credential    string
	originals     []deliveryOriginal
	completed     map[string]bool
	receipts      []string
	attempts      []string
	failReceipt   string
	beforeReceipt func() error
	offerEntered  chan struct{}
	offerRelease  <-chan struct{}
}

func (m *deliveryMailbox) state() (receipts, attempts, pending []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	receipts = append([]string(nil), m.receipts...)
	attempts = append([]string(nil), m.attempts...)
	for _, original := range m.originals {
		if !m.completed[original.id] {
			pending = append(pending, original.id)
		}
	}
	return
}

func (m *deliveryMailbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+m.token ||
		r.Header.Get("X-Agentmail-Expected-Credential") != m.credential {
		m.t.Error("mail request omitted this authenticated credential or its registration pin")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/mailboxes/logical-offer":
		q := r.URL.Query()
		limit, err := strconv.Atoi(q.Get("limit"))
		if q.Get("address") != deliveryMailboxAddress || q.Get("logical_recipient") != deliveryLogicalRecipient ||
			q.Get("after") != "0" || err != nil || limit != 3 {
			m.t.Error("offer changed its authenticated mailbox, explicit label, pending cursor or three-message bound")
			http.Error(w, "invalid selection", http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		var selected []deliveryOriginal
		more, bodyBytes := false, 0
		for _, original := range m.originals {
			if m.completed[original.id] {
				continue
			}
			if len(selected) == 0 && len(original.body) > 64<<10 {
				m.mu.Unlock()
				// The real legacy oversize response supplies no structured message ID.
				http.Error(w, "{\"error\":\"logical offer is too large\"}", http.StatusRequestEntityTooLarge)
				return
			}
			if len(selected) == limit || bodyBytes+len(original.body) > 64<<10 {
				more = true
				break
			}
			selected = append(selected, original)
			bodyBytes += len(original.body)
		}
		entered, release := m.offerEntered, m.offerRelease
		m.mu.Unlock()
		if entered != nil {
			select {
			case entered <- struct{}{}:
			case <-r.Context().Done():
				return
			}
		}
		if release != nil {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		messages := make([]any, 0, len(selected))
		var next int64
		for _, original := range selected {
			messages = append(messages, original.envelope())
			next = original.seq
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mailbox": map[string]any{
				"address": deliveryMailboxAddress, "revision": 2, "delivery_mode": "pull",
				"logical_recipients": []string{deliveryLogicalRecipient},
			},
			"logical_recipient": deliveryLogicalRecipient, "messages": messages,
			"next_after": next, "more": more, "body_bytes": bodyBytes, "max_body_bytes": 64 << 10,
		})
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/messages/") && strings.HasSuffix(r.URL.Path, "/read"):
		id, err := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/messages/"), "/read"))
		var input struct {
			Recipient        string `json:"recipient"`
			LogicalRecipient string `json:"logical_recipient"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err != nil || decoder.Decode(&input) != nil || input.Recipient != deliveryMailboxAddress ||
			input.LogicalRecipient != deliveryLogicalRecipient {
			m.t.Error("receipt changed its exact ID, owned mailbox or explicit logical selection")
			http.Error(w, "invalid receipt", http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		var original *deliveryOriginal
		for i := range m.originals {
			if m.originals[i].id == id {
				copy := m.originals[i]
				original = &copy
				break
			}
		}
		if original == nil {
			m.mu.Unlock()
			m.t.Error("receipt targeted an unoffered message ID")
			http.Error(w, "unknown message", http.StatusNotFound)
			return
		}
		m.attempts = append(m.attempts, id)
		fail, before := m.failReceipt == id, m.beforeReceipt
		m.mu.Unlock()
		if before != nil {
			if err := before(); err != nil {
				m.t.Error(err)
			}
		}
		if fail {
			http.Error(w, "private receipt service detail", http.StatusServiceUnavailable)
			return
		}
		m.mu.Lock()
		if !m.completed[id] {
			m.completed[id] = true
			m.receipts = append(m.receipts, id)
		}
		m.mu.Unlock()
		receipt := original.envelope()
		receipt["delivery_status"] = "completed"
		receipt["read_at"], receipt["ack_at"] = "2026-11-09T00:00:01Z", "2026-11-09T00:00:01Z"
		_ = json.NewEncoder(w).Encode(receipt)
	default:
		m.t.Errorf("mail used an unexpected route: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected route", http.StatusNotFound)
	}
}

type deliveryFixture struct {
	server   *Server
	remote   *httptest.Server
	token    string
	session  string
	mail     *deliveryMailbox
	ordinary wire.Result
	next     atomic.Int64
}

func deliveryOrdinary() wire.Result {
	return wire.Result{
		Meta: map[string]any{"ordinary_meta": "retained"},
		Content: []wire.Content{
			{"type": "text", "text": "delivery ordinary first item"},
			{"type": "image", "mimeType": "image/png", "data": "iVBORw0KGgo="},
			{"type": "resource_link", "uri": "https://example.invalid/original", "name": "original-resource", "mimeType": "text/plain"},
			{"type": "text", "text": "delivery ordinary final item"},
		},
		StructuredContent: map[string]any{"ordinary_data": []any{"one", float64(2)}},
		DashboardMetadata: map[string]any{"private_dashboard": "must remain private"},
	}
}

func newDeliveryFixture(t *testing.T, bodies ...string) *deliveryFixture {
	t.Helper()
	path, policy, tokens := namedPolicy(t)
	policy.Principals[0].Type, policy.Principals[0].Role = "system", ""
	policy.Agents[0].Kind, policy.Agents[0].Address = "leaf", deliveryMailboxAddress
	policy.Credentials[0].Audience = "both"
	savePolicy(t, path, policy)
	mail := &deliveryMailbox{
		t: t, token: tokens[0], credential: policy.Credentials[0].ID,
		completed: make(map[string]bool),
	}
	for i, body := range bodies {
		mail.originals = append(mail.originals, deliveryOriginal{
			id: fmt.Sprintf("delivery-original-%02d", i+1), seq: int64(i + 1), body: body,
		})
	}
	socket := logicalMailUnixHTTP(t, mail.ServeHTTP)
	ordinary := deliveryOrdinary()
	server, remote, _, _ := newHTTPFixture(t, func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		return ordinary, nil
	})
	server.Config.Set("principalsFile", path)
	server.Config.Set("logicalMailSocket", socket)
	server.Config.Set("logicalMailPrincipal", "alpha")
	server.Config.Set("logicalMailAgent", "crew-alpha")
	server.Config.Set("logicalMailMailbox", deliveryMailboxAddress)
	return &deliveryFixture{
		server: server, remote: remote, token: tokens[0],
		session: initialize(t, remote, tokens[0], "2025-11-25"), mail: mail, ordinary: ordinary,
	}
}

func (f *deliveryFixture) newRequest(ctx context.Context) (*http.Request, string) {
	id := fmt.Sprintf("delivery-rpc-%d", f.next.Add(1))
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": "read", "arguments": map[string]any{
			"workspace_id": "fixture", "path": "file", "agent": deliveryLogicalRecipient,
		}},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, f.remote.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Mcp-Session-Id", f.session)
	return req, id
}

// Recognize a whole result frame rather than an early SSE header/notification.
func deliveryResult(data []byte, id string) (map[string]any, bool) {
	decode := func(raw []byte) (map[string]any, bool) {
		var rpc map[string]any
		if json.Unmarshal(raw, &rpc) != nil || rpc["id"] != id {
			return nil, false
		}
		result, ok := rpc["result"].(map[string]any)
		return result, ok
	}
	if result, ok := decode(data); ok {
		return result, true
	}
	events := bytes.Split(data, []byte("\n\n"))
	for _, event := range events[:len(events)-1] {
		var payload []byte
		for _, line := range bytes.Split(event, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				payload = append(payload, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))...)
				payload = append(payload, '\n')
			}
		}
		if result, ok := decode(payload); ok {
			return result, true
		}
	}
	return nil, false
}

func (f *deliveryFixture) remoteCall(ctx context.Context) (map[string]any, error) {
	req, id := f.newRequest(ctx)
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("combined response status=%d read=%v", response.StatusCode, err)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return nil, fmt.Errorf("default SDK transport did not produce SSE: %q", response.Header.Get("Content-Type"))
	}
	result, ok := deliveryResult(data, id)
	if !ok {
		return nil, errors.New("combined SSE response omitted its complete result frame")
	}
	return result, nil
}

func deliveryPayload(t *testing.T, result map[string]any, ordinary wire.Result) (map[string]any, string) {
	t.Helper()
	encoded, err := json.Marshal(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]any
	_ = json.Unmarshal(encoded, &expected)
	before := expected["content"].([]any)
	actual, ok := result["content"].([]any)
	if !ok || len(actual) != len(before)+1 {
		t.Fatalf("standard content count=%d, want %d ordinary items plus separate mail text", len(actual), len(before))
	}
	if !reflect.DeepEqual(actual[:len(before)], before) {
		t.Fatal("SDK/SSE bridge lost or changed an ordinary standard content item")
	}
	copy := make(map[string]any, len(result))
	for key, value := range result {
		copy[key] = value
	}
	copy["content"] = actual[:len(before)]
	if !reflect.DeepEqual(copy, expected) {
		t.Fatal("mail changed ordinary structured content, metadata, errors or private fields")
	}
	item, ok := actual[len(before)].(map[string]any)
	text, textOK := item["text"].(string)
	if !ok || item["type"] != "text" || !textOK || len(text) > deliveryTextBudget {
		t.Fatalf("mail is not a complete standard text item within 16 KiB: bytes=%d", len(text))
	}
	if !strings.HasPrefix(text, logicalMailOfferPrefix) {
		t.Fatal("mail omitted its declared response envelope")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(text, logicalMailOfferPrefix)), &payload); err != nil {
		t.Fatalf("bounded mail text did not retain a complete JSON envelope: %v", err)
	}
	return payload, text
}

func deliveryRequireOriginals(t *testing.T, payload map[string]any, originals ...deliveryOriginal) {
	t.Helper()
	var got []any
	if payload["messages"] != nil {
		var ok bool
		got, ok = payload["messages"].([]any)
		if !ok {
			t.Fatal("mail messages are not an envelope array")
		}
	}
	if len(got) != len(originals) {
		t.Fatalf("included envelopes=%d, want %d", len(got), len(originals))
	}
	for i, original := range originals {
		raw, _ := json.Marshal(original.envelope())
		var expected any
		_ = json.Unmarshal(raw, &expected)
		if !reflect.DeepEqual(got[i], expected) {
			t.Fatalf("included envelope %d lost its original ID, sender, recipient, correlation or full body", i)
		}
	}
}

type deliveryGate struct {
	entered chan struct{}
	release chan struct{}
	enter   sync.Once
	leave   sync.Once
}

func newDeliveryGate(t *testing.T) *deliveryGate {
	t.Helper()
	gate := &deliveryGate{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(gate.open)
	return gate
}
func (g *deliveryGate) open() { g.leave.Do(func() { close(g.release) }) }
func (g *deliveryGate) hold(ctx context.Context) error {
	g.enter.Do(func() { close(g.entered) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-g.release:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
func deliveryAwait(t *testing.T, ch <-chan struct{}, reason string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal(reason)
	}
}

var errDeliveryWrite = errors.New("injected combined-response transport failure")

type deliveryWriter struct {
	header          http.Header
	mu              sync.Mutex
	body            bytes.Buffer
	status          int
	id              string
	ctx             context.Context
	mode            string
	writeGate       *deliveryGate
	flushGate       *deliveryGate
	terminalSeen    bool
	terminalWritten bool
	terminalFlushed bool
	flushFailed     bool
}

func (w *deliveryWriter) Header() http.Header { return w.header }
func (w *deliveryWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}
func (w *deliveryWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	prospective := append(append([]byte(nil), w.body.Bytes()...), data...)
	_, complete := deliveryResult(prospective, w.id)
	terminal := complete && !w.terminalSeen
	if terminal {
		w.terminalSeen = true
	}
	w.mu.Unlock()
	if terminal && w.writeGate != nil {
		if err := w.writeGate.hold(w.ctx); err != nil {
			return 0, err
		}
	}
	n, err := len(data), error(nil)
	if terminal {
		switch w.mode {
		case "zero-error":
			n, err = 0, errDeliveryWrite
		case "partial-error":
			n, err = len(data)/2, errDeliveryWrite
		case "short-nil":
			n = len(data) - 1
		case "full-error":
			err = errDeliveryWrite
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_, _ = w.body.Write(data[:n])
	if terminal && n == len(data) && err == nil {
		w.terminalWritten = true
	}
	return n, err
}
func (w *deliveryWriter) FlushError() error {
	w.mu.Lock()
	terminal := w.terminalWritten && !w.terminalFlushed
	w.mu.Unlock()
	if !terminal {
		return nil
	}
	if w.flushGate != nil {
		if err := w.flushGate.hold(w.ctx); err != nil {
			return err
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.mode == "flush-error" {
		w.flushFailed = true
		return errDeliveryWrite
	}
	w.terminalFlushed = true
	return nil
}
func (w *deliveryWriter) Flush() { _ = w.FlushError() }
func (w *deliveryWriter) receiptAllowed() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.terminalWritten || !w.terminalFlushed {
		return errors.New("receipt arrived before the full result frame and successful flush")
	}
	return nil
}
func (w *deliveryWriter) snapshot() ([]byte, bool, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.body.Bytes()...), w.terminalSeen, w.flushFailed
}
func (f *deliveryFixture) serve(ctx context.Context, w *deliveryWriter) {
	req, id := f.newRequest(ctx)
	w.id, w.ctx = id, ctx
	f.server.ServeHTTP(w, req)
}

func deliveryStart(t *testing.T, f *deliveryFixture, w *deliveryWriter) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		if w.writeGate != nil {
			w.writeGate.open()
		}
		if w.flushGate != nil {
			w.flushGate.open()
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("combined-response handler did not retire during cleanup")
		}
	})
	go func() { defer close(done); f.serve(ctx, w) }()
	return cancel, done
}

func deliveryIDs(originals []deliveryOriginal) []string {
	var ids []string
	for _, original := range originals {
		ids = append(ids, original.id)
	}
	return ids
}

func TestLogicalMailDeliveryWaitsForCompleteResponseAndFlush(t *testing.T) {
	f := newDeliveryFixture(t, "first complete body", "second complete body", "third complete body", "fourth omitted body")
	w := &deliveryWriter{header: make(http.Header), writeGate: newDeliveryGate(t), flushGate: newDeliveryGate(t)}
	f.mail.mu.Lock()
	f.mail.beforeReceipt = w.receiptAllowed
	f.mail.mu.Unlock()
	_, done := deliveryStart(t, f, w)
	deliveryAwait(t, w.writeGate.entered, "combined result never reached its terminal write")
	if _, attempts, _ := f.mail.state(); len(attempts) != 0 {
		t.Fatal("receipt preceded complete response write")
	}
	w.writeGate.open()
	deliveryAwait(t, w.flushGate.entered, "combined result never reached its final flush")
	if _, attempts, _ := f.mail.state(); len(attempts) != 0 {
		t.Fatal("receipt preceded successful response flush")
	}
	w.flushGate.open()
	deliveryAwait(t, done, "successful combined response did not finish")
	data, _, _ := w.snapshot()
	result, ok := deliveryResult(data, w.id)
	if !ok {
		t.Fatal("successful writer did not receive a whole SDK result")
	}
	payload, _ := deliveryPayload(t, result, f.ordinary)
	deliveryRequireOriginals(t, payload, f.mail.originals[:3]...)
	if payload["more"] != true {
		t.Fatal("bounded batch omitted its remainder notice")
	}
	receipts, attempts, pending := f.mail.state()
	if !reflect.DeepEqual(receipts, deliveryIDs(f.mail.originals[:3])) ||
		!reflect.DeepEqual(attempts, receipts) || !reflect.DeepEqual(pending, deliveryIDs(f.mail.originals[3:])) {
		t.Fatalf("receipt set differs from included messages: receipts=%v attempts=%v pending=%v", receipts, attempts, pending)
	}
}

func TestLogicalMailDeliveryFailedWritesRemainPending(t *testing.T) {
	for _, mode := range []string{"zero-error", "partial-error", "short-nil", "full-error", "flush-error"} {
		t.Run(mode, func(t *testing.T) {
			f := newDeliveryFixture(t, "full original after a failed outer response")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			w := &deliveryWriter{header: make(http.Header), mode: mode}
			f.serve(ctx, w)
			_, seen, flushFailed := w.snapshot()
			if !seen || mode == "flush-error" && !flushFailed {
				t.Fatal("fault did not reach the selected mail's terminal response boundary")
			}
			receipts, attempts, pending := f.mail.state()
			if len(receipts) != 0 || len(attempts) != 0 || !reflect.DeepEqual(pending, deliveryIDs(f.mail.originals)) {
				t.Fatal("failed or uncertain outer write receipted a pending original")
			}
			result, err := f.remoteCall(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := deliveryPayload(t, result, f.ordinary)
			deliveryRequireOriginals(t, payload, f.mail.originals...)
			receipts, _, pending = f.mail.state()
			if !reflect.DeepEqual(receipts, deliveryIDs(f.mail.originals)) || len(pending) != 0 {
				t.Fatal("later successful response could not complete the same retained original")
			}
		})
	}
}

func TestLogicalMailDeliveryCancelledAfterSelectionRemainsPending(t *testing.T) {
	f := newDeliveryFixture(t, "original selected before the client cancellation")
	w := &deliveryWriter{header: make(http.Header), writeGate: newDeliveryGate(t)}
	cancel, done := deliveryStart(t, f, w)
	deliveryAwait(t, w.writeGate.entered, "cancellation control did not reach the selected mail response")
	cancel()
	w.writeGate.open()
	deliveryAwait(t, done, "cancelled result writer did not retire")
	if receipts, attempts, pending := f.mail.state(); len(receipts) != 0 || len(attempts) != 0 || !reflect.DeepEqual(pending, deliveryIDs(f.mail.originals)) {
		t.Fatal("cancellation after offer selection completed pending mail")
	}
	result, err := f.remoteCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := deliveryPayload(t, result, f.ordinary)
	deliveryRequireOriginals(t, payload, f.mail.originals...)
}

func TestLogicalMailDeliveryReceiptErrorKeepsResponseAndPendingRemainder(t *testing.T) {
	f := newDeliveryFixture(t, "first delivered body", "second body with uncertain receipt", "third retained body")
	f.mail.mu.Lock()
	f.mail.failReceipt = f.mail.originals[1].id
	f.mail.mu.Unlock()
	result, err := f.remoteCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	payload, text := deliveryPayload(t, result, f.ordinary)
	deliveryRequireOriginals(t, payload, f.mail.originals...)
	if strings.Contains(text, "private receipt service detail") {
		t.Fatal("receipt failure damaged completed output with a private backend error")
	}
	receipts, attempts, pending := f.mail.state()
	if !reflect.DeepEqual(receipts, deliveryIDs(f.mail.originals[:1])) ||
		!reflect.DeepEqual(attempts, deliveryIDs(f.mail.originals[:2])) ||
		!reflect.DeepEqual(pending, deliveryIDs(f.mail.originals[1:])) {
		t.Fatalf("uncertain receipt did not stop at the existing per-ID boundary: receipts=%v attempts=%v pending=%v", receipts, attempts, pending)
	}
	f.mail.mu.Lock()
	f.mail.failReceipt = ""
	f.mail.mu.Unlock()
	result, err = f.remoteCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	payload, _ = deliveryPayload(t, result, f.ordinary)
	deliveryRequireOriginals(t, payload, f.mail.originals[1:]...)
	receipts, _, pending = f.mail.state()
	if !reflect.DeepEqual(receipts, deliveryIDs(f.mail.originals)) || len(pending) != 0 {
		t.Fatal("recovery lost an original ID or left its successful receipt unrecorded")
	}
}

func TestLogicalMailDeliveryBudgetsEncodedWholeEnvelopes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bodies []string
		count  int
	}{
		{"complete-unicode", []string{strings.Repeat("é", 2048)}, 1},
		{"first-body-over-text-cap", []string{strings.Repeat("x", deliveryTextBudget+1)}, 0},
		{"escaped-first-over-text-cap", []string{strings.Repeat("\x00", 3000)}, 0},
		{"later-envelope-does-not-fit", []string{"small first", strings.Repeat("x", deliveryTextBudget), "small third"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDeliveryFixture(t, tc.bodies...)
			result, err := f.remoteCall(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			payload, text := deliveryPayload(t, result, f.ordinary)
			deliveryRequireOriginals(t, payload, f.mail.originals[:tc.count]...)
			receipts, attempts, pending := f.mail.state()
			if !reflect.DeepEqual(receipts, deliveryIDs(f.mail.originals[:tc.count])) ||
				!reflect.DeepEqual(attempts, receipts) ||
				!reflect.DeepEqual(pending, deliveryIDs(f.mail.originals[tc.count:])) {
				t.Fatal("text budgeting receipted an omitted body or failed an included original")
			}
			if tc.count < len(tc.bodies) && payload["more"] != true {
				t.Fatal("text budget omitted the pending remainder")
			}
			if tc.count == 0 {
				if !strings.Contains(text, f.mail.originals[0].id) ||
					!strings.Contains(text, deliveryLogicalRecipient) ||
					!(strings.Contains(text, "/v1/messages/") || strings.Contains(text, "agentmail_read")) {
					t.Fatal("oversized first envelope lacks an existing exact-ID, exact-label fetch path")
				}
			}
		})
	}
}

func TestLogicalMailDeliveryBackendOversizeKeepsScopedFetchNotice(t *testing.T) {
	f := newDeliveryFixture(t, strings.Repeat("x", (64<<10)+1))
	result, err := f.remoteCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	payload, text := deliveryPayload(t, result, f.ordinary)
	deliveryRequireOriginals(t, payload)
	if !strings.Contains(text, deliveryMailboxAddress) || !strings.Contains(text, deliveryLogicalRecipient) ||
		!(strings.Contains(text, "/v1/messages?") || strings.Contains(text, "agentmail_list")) {
		t.Fatal("backend 413 lacks a usable selected-mailbox fetch notice without parsing an error ID")
	}
	if receipts, attempts, pending := f.mail.state(); len(receipts) != 0 || len(attempts) != 0 || !reflect.DeepEqual(pending, deliveryIDs(f.mail.originals)) {
		t.Fatal("backend oversize refusal completed an omitted original")
	}
}

func TestLogicalMailDeliveryConcurrentResponsesKeepIdempotentOriginals(t *testing.T) {
	f := newDeliveryFixture(t, "one original offered to concurrent successful responses")
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var releaseOnce sync.Once
	open := func() { releaseOnce.Do(func() { close(release) }) }
	f.mail.mu.Lock()
	f.mail.offerEntered, f.mail.offerRelease = entered, release
	f.mail.mu.Unlock()
	type outcome struct {
		result map[string]any
		err    error
	}
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan outcome, 2)
	finished := make(chan struct{}, 2)
	t.Cleanup(func() {
		cancel()
		open()
		for range 2 {
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Error("concurrent response did not retire during cleanup")
			}
		}
	})
	for range 2 {
		go func() {
			defer func() { finished <- struct{}{} }()
			result, err := f.remoteCall(ctx)
			results <- outcome{result, err}
		}()
	}
	for range 2 {
		deliveryAwait(t, entered, "concurrent requests did not retain both original offers")
	}
	open()
	for range 2 {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatal(got.err)
			}
			payload, _ := deliveryPayload(t, got.result, f.ordinary)
			deliveryRequireOriginals(t, payload, f.mail.originals...)
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent combined response did not finish")
		}
	}
	receipts, attempts, pending := f.mail.state()
	if !reflect.DeepEqual(receipts, deliveryIDs(f.mail.originals)) || len(attempts) != 2 ||
		attempts[0] != f.mail.originals[0].id || attempts[1] != f.mail.originals[0].id || len(pending) != 0 {
		t.Fatalf("duplicate response changed original identity or completion: receipts=%v attempts=%v pending=%v", receipts, attempts, pending)
	}
}
