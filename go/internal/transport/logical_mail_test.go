//go:build unix

package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
)

const logicalMailHelperAddress = "fixture/fleet"
const logicalMailHelperAuth = "Bearer fixture-current-request"

func newLogicalMailHelperFixture(t *testing.T, handler http.HandlerFunc, observeState ...func(net.Conn, http.ConnState)) (*Server, wire.Identity) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cp-mail-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "mail.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	remote := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	if len(observeState) != 0 {
		remote.ConnState = observeState[0]
	}
	stopped := make(chan error, 1)
	go func() { stopped <- remote.Serve(listener) }()
	t.Cleanup(func() {
		_ = remote.Close()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Error("private logical-mail endpoint did not stop")
		}
	})
	cfg := config.New(filepath.Join(dir, "unused-state"))
	cfg.Set("logicalMailSocket", socket)
	cfg.Set("logicalMailPrincipal", "fixture-principal")
	cfg.Set("logicalMailAgent", "fixture-agent")
	cfg.Set("logicalMailMailbox", logicalMailHelperAddress)
	id := wire.Identity{
		Source: "named", PrincipalType: "system", PrincipalID: "fixture-principal",
		CredentialID: "fixture-credential", AgentID: "fixture-agent", AgentKind: "leaf",
	}
	return &Server{Config: cfg}, id
}

func logicalMailHelperOffer(label string, bodies ...string) map[string]any {
	messages := make([]any, 0, len(bodies))
	size, last := 0, int64(0)
	for i, body := range bodies {
		last = int64(101 + i*2)
		messages = append(messages, map[string]any{
			"seq": last, "id": fmt.Sprintf("original-%d", i+1),
			"from": "fixture/sender", "to": logicalMailHelperAddress,
			"logical_sender": "ROOT", "logical_recipient": label,
			"body": body, "reply_to": "original-parent", "mode": "later",
			"origin":                map[string]any{"agent_id": "sender-registration"},
			"future_envelope_field": map[string]any{"preserved": true},
		})
		size += len(body)
	}
	return map[string]any{
		"mailbox": map[string]any{
			"address": logicalMailHelperAddress, "revision": 7,
			"logical_recipients": []string{"SP3", "SP6", "SP7", "sp6", "A-1"},
		},
		"logical_recipient": label, "messages": messages,
		"next_after": last, "more": false, "body_bytes": size, "max_body_bytes": logicalMailMaxBodyBytes,
	}
}

func logicalMailHelperJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func logicalMailHelperResult() wire.Result {
	return wire.Result{
		Meta:              map[string]any{"existing": "metadata"},
		Content:           []wire.Content{{"type": "text", "text": "ordinary tool result"}},
		StructuredContent: map[string]any{"existing": []any{"structured", 3}},
		IsError:           true,
		DashboardMetadata: map[string]any{"private": "audit metadata"},
	}
}

func logicalMailHelperPayload(t *testing.T, result wire.Result) map[string]any {
	t.Helper()
	want := logicalMailHelperResult()
	if len(result.Content) != len(want.Content)+1 {
		t.Fatalf("content items=%d; want exactly one appended offer", len(result.Content))
	}
	content := result.Content[len(result.Content)-1]
	if content["type"] != "text" {
		t.Fatal("offer was not one explicit text item")
	}
	text, ok := content["text"].(string)
	if !ok || !strings.HasPrefix(text, logicalMailOfferPrefix) || len(text) > logicalMailMaxTextBytes {
		t.Fatal("attachment omitted its delivery explanation or exceeded 16 KiB")
	}
	result.Content = result.Content[:len(want.Content)]
	if !reflect.DeepEqual(result, want) {
		t.Fatal("mail attachment changed ordinary result fields")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(text, logicalMailOfferPrefix)), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestLogicalMailHelperPreservesOriginalEnvelopesAndRepeatsOffers(t *testing.T) {
	var calls, proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxyCalls.Add(1) }))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	offer := logicalMailHelperOffer("SP6", "complete first body\n第二行", "complete second body")
	offer["more"] = true
	encoded := logicalMailHelperJSON(t, offer)
	server, id := newLogicalMailHelperFixture(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/mailboxes/logical-offer" || r.Host != "agentmail.local" {
			t.Errorf("unexpected logical offer request: %s %s", r.Method, r.URL.Path)
		}
		if query := r.URL.Query(); len(query) != 4 || query.Get("address") != logicalMailHelperAddress ||
			query.Get("logical_recipient") != "SP6" || query.Get("after") != "0" || query.Get("limit") != "3" {
			t.Errorf("request changed its exact selection or advanced a cursor: %v", query)
		}
		if want := fmt.Sprintf("Bearer current-request-%d", n); r.Header.Get("Authorization") != want {
			t.Error("helper did not forward this request's exact Authorization")
		}
		if r.Header.Get("X-Agentmail-Expected-Credential") != "fixture-credential" {
			t.Error("helper omitted the authenticated registration pin")
		}
		_, _ = w.Write(encoded)
	})
	base := logicalMailHelperResult()
	backing := make([]wire.Content, 2, 3)
	backing[0], backing[1] = base.Content[0], wire.Content{"sentinel": "caller-owned spare capacity"}
	base.Content = backing[:1]
	for n := 1; n <= 2; n++ {
		result := server.attachLogicalMail(context.Background(), fmt.Sprintf("Bearer current-request-%d", n), id,
			map[string]any{"agent": "SP6", "after": 999, "logical_recipient": "SP7", "url": proxy.URL, "socket": "/not-used", "mailbox": "not-used"}, base)
		payload := logicalMailHelperPayload(t, result)
		var expected map[string]any
		_ = json.Unmarshal(encoded, &expected)
		if !reflect.DeepEqual(payload["messages"], expected["messages"]) || payload["next_after"] != expected["next_after"] || payload["more"] != true {
			t.Fatal("original ID/from/to/reply/body or offer pagination metadata changed")
		}
		if !reflect.DeepEqual(base, logicalMailHelperResult()) || backing[1]["sentinel"] != "caller-owned spare capacity" {
			t.Fatal("helper mutated caller-owned result storage")
		}
	}
	if calls.Load() != 2 || proxyCalls.Load() != 0 {
		t.Fatalf("offer calls=%d proxy calls=%d; want two offers and no receipt, retry, paging or proxy", calls.Load(), proxyCalls.Load())
	}
}

func TestLogicalMailHelperRequiresExplicitLabelAndCurrentNamedIdentity(t *testing.T) {
	var calls atomic.Int32
	server, id := newLogicalMailHelperFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	})
	base := logicalMailHelperResult()
	for _, label := range []any{nil, "", 6, "SP 6", "SP_6", "SP/6", "ABCDE", "é", "SP6\n"} {
		t.Run(fmt.Sprintf("label-%v", label), func(t *testing.T) {
			got := server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": label}, base)
			if !reflect.DeepEqual(got, base) {
				t.Fatal("invalid label changed the ordinary result")
			}
		})
	}
	for _, mutate := range []func(*wire.Identity){
		func(v *wire.Identity) { *v = wire.Identity{} },
		func(v *wire.Identity) { v.Source = "legacy" },
		func(v *wire.Identity) { v.PrincipalType = "person" },
		func(v *wire.Identity) { v.PrincipalID = "another-principal" },
		func(v *wire.Identity) { v.AgentID = "another-agent" },
		func(v *wire.Identity) { v.CredentialID = "" },
		func(v *wire.Identity) { v.AgentKind = "unknown" },
	} {
		other := id
		mutate(&other)
		got := server.attachLogicalMail(context.Background(), logicalMailHelperAuth, other, map[string]any{"agent": "SP6"}, base)
		if !reflect.DeepEqual(got, base) {
			t.Fatal("ineligible current identity changed the ordinary result")
		}
	}
	for _, key := range []string{"logicalMailSocket", "logicalMailPrincipal", "logicalMailAgent", "logicalMailMailbox"} {
		value := server.Config.String(key, "")
		server.Config.Set(key, "")
		if got := server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, base); !reflect.DeepEqual(got, base) {
			t.Fatal("partial startup configuration changed the ordinary result")
		}
		server.Config.Set(key, value)
	}
	for _, authorization := range []string{"", " ", "Bearer fixture\r\nAnother: value"} {
		if got := server.attachLogicalMail(context.Background(), authorization, id, map[string]any{"agent": "SP6"}, base); !reflect.DeepEqual(got, base) {
			t.Fatal("invalid request authorization changed the result")
		}
	}
	originalSocket := server.Config.String("logicalMailSocket", "")
	for _, path := range []string{"relative.sock", "http://remote.invalid/socket", originalSocket + "\n"} {
		server.Config.Set("logicalMailSocket", path)
		_ = server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, base)
	}
	server.Config.Set("logicalMailSocket", originalSocket)
	_ = server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, nil, base)
	_ = (&Server{}).attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, base)
	_ = (&Server{Config: config.New(t.TempDir())}).attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, base)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = server.attachLogicalMail(ctx, logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, base)
	if calls.Load() != 0 {
		t.Fatalf("ineligible requests contacted the endpoint %d times", calls.Load())
	}
}

func TestLogicalMailHelperRejectsInvalidOffers(t *testing.T) {
	first := func(offer map[string]any) map[string]any { return offer["messages"].([]any)[0].(map[string]any) }
	mailbox := func(offer map[string]any) map[string]any { return offer["mailbox"].(map[string]any) }
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"other-mailbox", func(o map[string]any) { mailbox(o)["address"] = "fixture/other" }},
		{"other-label", func(o map[string]any) { o["logical_recipient"] = "SP7" }},
		{"unconfigured-label", func(o map[string]any) { mailbox(o)["logical_recipients"] = []string{"SP7"} }},
		{"duplicate-labels", func(o map[string]any) { mailbox(o)["logical_recipients"] = []string{"SP6", "SP6"} }},
		{"invalid-configured-label", func(o map[string]any) { mailbox(o)["logical_recipients"] = []string{"SP6", "SP 7"} }},
		{"missing-revision", func(o map[string]any) { delete(mailbox(o), "revision") }},
		{"null-messages", func(o map[string]any) { o["messages"] = nil }},
		{"missing-body", func(o map[string]any) { delete(first(o), "body") }},
		{"null-body", func(o map[string]any) { first(o)["body"] = nil }},
		{"numeric-body", func(o map[string]any) { first(o)["body"] = 3 }},
		{"wrong-message-recipient", func(o map[string]any) { first(o)["to"] = "fixture/other" }},
		{"wrong-message-label", func(o map[string]any) { first(o)["logical_recipient"] = "SP7" }},
		{"empty-id", func(o map[string]any) { first(o)["id"] = "" }},
		{"empty-from", func(o map[string]any) { first(o)["from"] = "" }},
		{"zero-sequence", func(o map[string]any) { first(o)["seq"] = 0 }},
		{"wrong-body-byte-count", func(o map[string]any) { o["body_bytes"] = 0 }},
		{"oversize-body", func(o map[string]any) {
			first(o)["body"] = strings.Repeat("x", logicalMailMaxBodyBytes+1)
			o["body_bytes"] = logicalMailMaxBodyBytes + 1
		}},
		{"oversize-total", func(o map[string]any) {
			one := first(o)
			one["body"] = strings.Repeat("x", logicalMailMaxBodyBytes)
			two := map[string]any{"seq": 102, "id": "another", "from": "fixture/sender", "to": logicalMailHelperAddress, "logical_recipient": "SP6", "body": "x"}
			o["messages"], o["next_after"], o["body_bytes"] = []any{one, two}, 102, logicalMailMaxBodyBytes+1
		}},
		{"too-many-messages", func(o map[string]any) {
			bodies := make([]string, logicalMailMaxMessages+1)
			for i := range bodies {
				bodies[i] = "x"
			}
			replacement := logicalMailHelperOffer("SP6", bodies...)
			for key, value := range replacement {
				o[key] = value
			}
		}},
		{"duplicate-original-id", func(o map[string]any) {
			replacement := logicalMailHelperOffer("SP6", "first", "second")
			replacement["messages"].([]any)[1].(map[string]any)["id"] = "original-1"
			for key, value := range replacement {
				o[key] = value
			}
		}},
		{"out-of-order-sequence", func(o map[string]any) {
			replacement := logicalMailHelperOffer("SP6", "first", "second")
			replacement["messages"].([]any)[1].(map[string]any)["seq"] = 100
			replacement["next_after"] = 100
			for key, value := range replacement {
				o[key] = value
			}
		}},
		{"missing-next-after", func(o map[string]any) { delete(o, "next_after") }},
		{"conflicting-next-after", func(o map[string]any) { o["next_after"] = 999 }},
		{"null-more", func(o map[string]any) { o["more"] = nil }},
		{"missing-byte-count", func(o map[string]any) { delete(o, "body_bytes") }},
		{"unknown-byte-limit", func(o map[string]any) { o["max_body_bytes"] = logicalMailMaxBodyBytes + 1 }},
		{"null-reply", func(o map[string]any) { first(o)["reply_to"] = nil }},
		{"numeric-reply", func(o map[string]any) { first(o)["reply_to"] = 42 }},
		{"oversize-wire", func(o map[string]any) { o["padding"] = strings.Repeat("x", logicalMailMaxWireBytes) }},
		{"empty-with-more", func(o map[string]any) {
			o["messages"], o["next_after"], o["body_bytes"], o["more"] = []any{}, 0, 0, true
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			offer := logicalMailHelperOffer("SP6", "body")
			test.change(offer)
			encoded := logicalMailHelperJSON(t, offer)
			var calls atomic.Int32
			server, id := newLogicalMailHelperFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/v1/mailboxes/logical-offer" {
					t.Error("refused offer caused a receipt or unrelated request")
				}
				_, _ = w.Write(encoded)
			})
			base := logicalMailHelperResult()
			got := server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, base)
			if !reflect.DeepEqual(got, base) || calls.Load() != 1 {
				t.Fatalf("invalid offer changed result or caused extra calls: calls=%d", calls.Load())
			}
		})
	}
}

func TestLogicalMailHelperRejectsLossyOrConflictingJSON(t *testing.T) {
	base := logicalMailHelperJSON(t, logicalMailHelperOffer("SP6", "�"))
	cases := []struct {
		name string
		body []byte
	}{
		{"invalid-utf8", bytes.Replace(base, []byte("�"), []byte{0xff}, 1)},
		{"unpaired-high-surrogate", bytes.Replace(base, []byte("�"), []byte("\\ud800"), 1)},
		{"unpaired-low-surrogate", bytes.Replace(base, []byte("�"), []byte("\\udc00"), 1)},
		{"truncated-json", base[:len(base)-1]},
		{"trailing-json", append(append([]byte(nil), base...), []byte("{}")...)},
		{"duplicate-routing", bytes.Replace(base, []byte("\"logical_recipient\":\"SP6\""), []byte("\"logical_recipient\":\"SP7\",\"logical_recipient\":\"SP6\""), 1)},
		{"case-conflicting-body", bytes.Replace(base, []byte("\"body\":\"�\""), []byte("\"Body\":\"other\",\"body\":\"�\""), 1)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server, id := newLogicalMailHelperFixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(test.body) })
			result := logicalMailHelperResult()
			if got := server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, result); !reflect.DeepEqual(got, result) {
				t.Fatal("malformed original content was repaired or partially offered")
			}
		})
	}
}

func TestLogicalMailHelperPreservesOrDefersCompleteBoundaryBodies(t *testing.T) {
	for _, tc := range []struct {
		body     string
		deferred bool
	}{
		{strings.Repeat("é", logicalMailMaxBodyBytes/2), true},
		{strings.Repeat("\x00", logicalMailMaxBodyBytes), true},
		{"😀", false},
		{"\\ud800", false},
	} {
		body := tc.body
		t.Run(fmt.Sprintf("bytes-%d-first-%x", len(body), body[0]), func(t *testing.T) {
			started := time.Now()
			encodeElapsed, preflightElapsed := time.Duration(-1), time.Duration(-1)
			setupElapsed, attachBegin, attachElapsed := time.Duration(-1), time.Duration(-1), time.Duration(-1)
			wireBytes, contentItems := -1, -1
			var phases struct {
				getConn, connectStart, connectDone, gotConn, wroteRequest, firstByte atomic.Int64
				serverNew, serverActive, serverClosed, handlerBegin, handlerEnd      atomic.Int64
				newCount, activeCount, closedCount                                   atomic.Int32
				writeBytes                                                           atomic.Int64
				connectError, requestError, writeError, serverContextError           atomic.Value
			}
			phases.writeBytes.Store(-1)
			stamp := func(dst *atomic.Int64) { dst.CompareAndSwap(0, time.Since(started).Nanoseconds()) }
			ms := func(elapsed time.Duration) float64 {
				if elapsed < 0 {
					return -1
				}
				return float64(elapsed) / float64(time.Millisecond)
			}
			eventMS := func(src *atomic.Int64) float64 {
				if elapsed := src.Load(); elapsed != 0 {
					return ms(time.Duration(elapsed))
				}
				return -1
			}
			errorClass := func(err error) string {
				var timeout net.Error
				switch {
				case err == nil:
					return "none"
				case errors.Is(err, context.Canceled):
					return "canceled"
				case errors.Is(err, context.DeadlineExceeded):
					return "deadline"
				case errors.As(err, &timeout) && timeout.Timeout():
					return "timeout"
				case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
					return "eof"
				case errors.Is(err, net.ErrClosed):
					return "closed"
				default:
					return "other"
				}
			}
			observedError := func(src *atomic.Value) string {
				if value := src.Load(); value != nil {
					return value.(string)
				}
				return "unobserved"
			}
			// This defer runs before fixture cleanup. Missing phases remain
			// unobserved; do not wait for them or include cleanup-induced events.
			defer func() {
				if !t.Failed() {
					return
				}
				t.Logf("mail-boundary phase_ms encode=%.3f preflight=%.3f setup=%.3f attach_begin=%.3f attach=%.3f budget=%.3f unobserved_ms=-1 wire_bytes=%d content_items=%d",
					ms(encodeElapsed), ms(preflightElapsed), ms(setupElapsed), ms(attachBegin), ms(attachElapsed), ms(logicalMailTimeout), wireBytes, contentItems)
				t.Logf("mail-boundary client_offsets_ms get_conn=%.3f connect_start=%.3f connect_done=%.3f got_conn=%.3f wrote_request=%.3f first_byte=%.3f connect_error=%s request_error=%s",
					eventMS(&phases.getConn), eventMS(&phases.connectStart), eventMS(&phases.connectDone), eventMS(&phases.gotConn), eventMS(&phases.wroteRequest), eventMS(&phases.firstByte),
					observedError(&phases.connectError), observedError(&phases.requestError))
				t.Logf("mail-boundary server_offsets_ms new=%.3f active=%.3f closed=%.3f handler_begin=%.3f handler_end=%.3f new_count=%d active_count=%d closed_count=%d write_bytes=%d write_error=%s server_context_after_write=%s",
					eventMS(&phases.serverNew), eventMS(&phases.serverActive), eventMS(&phases.serverClosed), eventMS(&phases.handlerBegin), eventMS(&phases.handlerEnd),
					phases.newCount.Load(), phases.activeCount.Load(), phases.closedCount.Load(), phases.writeBytes.Load(), observedError(&phases.writeError), observedError(&phases.serverContextError))
			}()
			encoded := logicalMailHelperJSON(t, logicalMailHelperOffer("SP6", body))
			if body == "😀" {
				encoded = bytes.Replace(encoded, []byte(body), []byte("\\ud83d\\ude00"), 1)
			}
			wireBytes = len(encoded)
			encodeElapsed = time.Since(started)
			preflightStarted := time.Now()
			offer, valid := decodeLogicalMailOffer(encoded, logicalMailHelperAddress, "SP6")
			preflightElapsed = time.Since(preflightStarted)
			if !valid || len(offer.Messages) != 1 {
				t.Fatal("valid backend boundary envelope was rejected")
			}
			setupStarted := time.Now()
			server, id := newLogicalMailHelperFixture(t, func(w http.ResponseWriter, r *http.Request) {
				stamp(&phases.handlerBegin)
				n, err := w.Write(encoded)
				phases.writeBytes.Store(int64(n))
				phases.writeError.Store(errorClass(err))
				phases.serverContextError.Store(errorClass(r.Context().Err()))
				stamp(&phases.handlerEnd)
			}, func(_ net.Conn, state http.ConnState) {
				switch state {
				case http.StateNew:
					phases.newCount.Add(1)
					stamp(&phases.serverNew)
				case http.StateActive:
					phases.activeCount.Add(1)
					stamp(&phases.serverActive)
				case http.StateClosed:
					phases.closedCount.Add(1)
					stamp(&phases.serverClosed)
				}
			})
			setupElapsed = time.Since(setupStarted)
			trace := &httptrace.ClientTrace{
				GetConn:      func(string) { stamp(&phases.getConn) },
				ConnectStart: func(string, string) { stamp(&phases.connectStart) },
				ConnectDone: func(_ string, _ string, err error) {
					phases.connectError.Store(errorClass(err))
					stamp(&phases.connectDone)
				},
				GotConn: func(httptrace.GotConnInfo) { stamp(&phases.gotConn) },
				WroteRequest: func(info httptrace.WroteRequestInfo) {
					phases.requestError.Store(errorClass(info.Err))
					stamp(&phases.wroteRequest)
				},
				GotFirstResponseByte: func() { stamp(&phases.firstByte) },
			}
			// The trace reaches the helper's child context, but does not expose
			// its deadline/error or a client body-read/decode completion phase.
			ctx := httptrace.WithClientTrace(context.Background(), trace)
			attachStarted := time.Now()
			attachBegin = time.Since(started)
			got := server.attachLogicalMail(ctx, logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, logicalMailHelperResult())
			attachElapsed = time.Since(attachStarted)
			contentItems = len(got.Content)
			payload := logicalMailHelperPayload(t, got)
			messages := payload["messages"].([]any)
			if tc.deferred {
				if len(messages) != 0 || payload["more"] != true {
					t.Fatal("oversized original was partially attached or lost its pending notice")
				}
				fetch := payload["fetch"].(map[string]any)
				args := fetch["arguments"].(map[string]any)
				if fetch["tool"] != "agentmail_read" || args["id"] != "original-1" || args["logical_recipient"] != "SP6" {
					t.Fatal("deferred original lost its exact-ID, exact-label fetch")
				}
				return
			}
			if len(messages) != 1 || messages[0].(map[string]any)["body"] != body || payload["body_bytes"] != float64(len(body)) {
				t.Fatal("complete boundary body was truncated or repaired")
			}
		})
	}
}

func TestLogicalMailHelperRevalidatesConfigurationAndDoesNotInheritLabel(t *testing.T) {
	var calls atomic.Int32
	server, id := newLogicalMailHelperFixture(t, func(w http.ResponseWriter, r *http.Request) {
		offer := logicalMailHelperOffer(r.URL.Query().Get("logical_recipient"), "pending")
		if calls.Add(1) > 1 {
			offer["mailbox"].(map[string]any)["revision"] = 8
			offer["mailbox"].(map[string]any)["logical_recipients"] = []string{"SP7"}
		}
		_, _ = w.Write(logicalMailHelperJSON(t, offer))
	})
	base := logicalMailHelperResult()
	_ = logicalMailHelperPayload(t, server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, base))
	if got := server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, nil, base); !reflect.DeepEqual(got, base) || calls.Load() != 1 {
		t.Fatal("unlabeled request inherited the earlier selection")
	}
	if got := server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, base); !reflect.DeepEqual(got, base) || calls.Load() != 2 {
		t.Fatal("helper reused prior configuration membership")
	}
}

func TestLogicalMailHelperPreservesResultOnEndpointFailure(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server, id := newLogicalMailHelperFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "private endpoint error: /private/socket; Bearer fixture-secret", status)
			})
			base := logicalMailHelperResult()
			if got := server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, base); !reflect.DeepEqual(got, base) {
				t.Fatal("endpoint error leaked into the ordinary tool result")
			}
		})
	}
	t.Run("unavailable", func(t *testing.T) {
		server, id := newLogicalMailHelperFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("wrong socket contacted") })
		server.Config.Set("logicalMailSocket", server.Config.String("logicalMailSocket", "")+".missing")
		base := logicalMailHelperResult()
		if got := server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, base); !reflect.DeepEqual(got, base) {
			t.Fatal("unavailable socket changed the result")
		}
	})
	t.Run("redirect", func(t *testing.T) {
		var redirected atomic.Int32
		other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
		t.Cleanup(other.Close)
		server, id := newLogicalMailHelperFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) })
		base := logicalMailHelperResult()
		if got := server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, base); !reflect.DeepEqual(got, base) || redirected.Load() != 0 {
			t.Fatal("logical-mail client followed a redirect or exposed its error")
		}
	})
	t.Run("empty", func(t *testing.T) {
		encoded := logicalMailHelperJSON(t, logicalMailHelperOffer("SP6"))
		server, id := newLogicalMailHelperFixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(encoded) })
		base := logicalMailHelperResult()
		if got := server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, base); !reflect.DeepEqual(got, base) {
			t.Fatal("empty offer added content")
		}
	})
}

func TestLogicalMailHelperHonorsCancellationDuringBodyRead(t *testing.T) {
	started := make(chan struct{})
	server, id := newLogicalMailHelperFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{"))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan wire.Result, 1)
	go func() {
		done <- server.attachLogicalMail(ctx, logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, logicalMailHelperResult())
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("private endpoint was not reached")
	}
	cancel()
	select {
	case got := <-done:
		if !reflect.DeepEqual(got, logicalMailHelperResult()) {
			t.Fatal("cancelled offer changed the result")
		}
	case <-time.After(time.Second):
		t.Fatal("mail body read ignored caller cancellation")
	}
}

func TestLogicalMailHelperBoundsLocalWait(t *testing.T) {
	started := make(chan struct{})
	server, id := newLogicalMailHelperFixture(t, func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	done := make(chan wire.Result, 1)
	go func() {
		done <- server.attachLogicalMail(context.Background(), logicalMailHelperAuth, id, map[string]any{"agent": "SP6"}, logicalMailHelperResult())
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("private endpoint was not reached")
	}
	select {
	case got := <-done:
		if !reflect.DeepEqual(got, logicalMailHelperResult()) {
			t.Fatal("local timeout changed the result")
		}
	case <-time.After(logicalMailTimeout + 2*time.Second):
		t.Fatal("logical-mail endpoint exceeded its bounded local wait")
	}
}
