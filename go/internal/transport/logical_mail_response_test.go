//go:build linux

package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Prepare through the real authenticated offer path. The response tests below
// exercise the observer at its writer boundary, then inspect the fake backend's
// durable originals rather than the observer's internal eligibility flags.
func responsePreparedMail(t *testing.T, f *deliveryFixture, ctx context.Context) (*logicalMailRequest, map[string]any, string) {
	t.Helper()
	identity, _, ok := f.server.authenticate(f.token)
	if !ok {
		t.Fatal("fixture credential was not authenticated")
	}
	result, delivery := f.server.prepareLogicalMail(ctx, "Bearer "+f.token, identity,
		map[string]any{"agent": deliveryLogicalRecipient}, f.ordinary)
	if delivery == nil {
		t.Fatal("authenticated offer did not prepare an original")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err := json.Unmarshal(encoded, &actual); err != nil {
		t.Fatal(err)
	}
	payload, text := deliveryPayload(t, actual, f.ordinary)
	deliveryRequireOriginals(t, payload, f.mail.originals...)
	responseRequireState(t, f, false)
	return &logicalMailRequest{ctx: ctx, delivery: delivery}, actual, text
}

func responseRequireState(t *testing.T, f *deliveryFixture, delivered bool) {
	t.Helper()
	receipts, attempts, pending := f.mail.state()
	originalIDs := deliveryIDs(f.mail.originals)
	if delivered {
		if !reflect.DeepEqual(receipts, originalIDs) || !reflect.DeepEqual(attempts, originalIDs) || len(pending) != 0 {
			t.Fatalf("complete response receipts=%v attempts=%v pending=%v, want exactly %v", receipts, attempts, pending, originalIDs)
		}
		return
	}
	if len(receipts) != 0 || len(attempts) != 0 || !reflect.DeepEqual(pending, originalIDs) {
		t.Fatalf("ineligible response receipts=%v attempts=%v pending=%v, want all originals pending", receipts, attempts, pending)
	}
}

func responseEvent(t *testing.T, id json.RawMessage, result map[string]any, change func(map[string]any)) []byte {
	t.Helper()
	frame := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	if change != nil {
		change(frame)
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	event := append([]byte("event: message\ndata: "), encoded...)
	return append(event, '\n', '\n')
}

func responseWriteEvent(t *testing.T, writer *logicalMailResponseWriter, event []byte) {
	t.Helper()
	writer.WriteHeader(http.StatusOK)
	if n, err := writer.Write(event); err != nil || n != len(event) {
		t.Fatalf("fixture complete write returned n=%d err=%v", n, err)
	}
}

func TestLogicalMailResponseRequiresMatchingFinalResult(t *testing.T) {
	tests := []struct {
		name       string
		requestID  string
		responseID string
		change     func(map[string]any)
		delivered  bool
	}{
		{name: "normalized_numeric_id", requestID: "17.0", responseID: "17", delivered: true},
		{name: "numeric_request_string_response", requestID: "17", responseID: "\"17\""},
		{name: "string_request_numeric_response", requestID: "\"17\"", responseID: "17"},
		{name: "different_string_response", requestID: "\"original\"", responseID: "\"other\""},
		{
			name: "outer_error", requestID: "\"original\"", responseID: "\"original\"",
			change: func(frame map[string]any) {
				delete(frame, "result")
				frame["error"] = map[string]any{"code": -32603, "message": "terminal protocol failure"}
			},
		},
		{
			name: "outer_error_even_with_full_result", requestID: "\"original\"", responseID: "\"original\"",
			change: func(frame map[string]any) {
				frame["error"] = map[string]any{"code": -32603, "message": "terminal protocol failure"}
			},
		},
		{
			name: "altered_final_mail_text", requestID: "\"original\"", responseID: "\"original\"",
			change: func(frame map[string]any) {
				content := frame["result"].(map[string]any)["content"].([]any)
				item := content[len(content)-1].(map[string]any)
				item["text"] = item["text"].(string) + "changed"
			},
		},
		{
			name: "missing_final_mail_item", requestID: "\"original\"", responseID: "\"original\"",
			change: func(frame map[string]any) {
				result := frame["result"].(map[string]any)
				content := result["content"].([]any)
				result["content"] = content[:len(content)-1]
			},
		},
		{
			name: "mail_is_not_final_item", requestID: "\"original\"", responseID: "\"original\"",
			change: func(frame map[string]any) {
				result := frame["result"].(map[string]any)
				content := result["content"].([]any)
				result["content"] = append(content, map[string]any{"type": "text", "text": "different final item"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newDeliveryFixture(t, "retain this exact original until its own complete response")
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			request, result, _ := responsePreparedMail(t, f, ctx)
			output := httptest.NewRecorder()
			writer := &logicalMailResponseWriter{
				ResponseWriter: output, ctx: ctx, request: request, rpcID: json.RawMessage(test.requestID),
			}
			event := responseEvent(t, json.RawMessage(test.responseID), result, test.change)
			responseWriteEvent(t, writer, event)
			if err := writer.FlushError(); err != nil {
				t.Fatal(err)
			}
			writer.complete(f.server.ctx)
			if !bytes.Equal(output.Body.Bytes(), event) {
				t.Fatal("receipt eligibility changed the ordinary response bytes")
			}
			responseRequireState(t, f, test.delivered)
		})
	}
}

// Deliberately exposes neither Flush nor Unwrap. A successful Write alone
// cannot establish that the complete response reached a flush boundary.
type responseWriteOnly struct {
	recorder *httptest.ResponseRecorder
}

func (w *responseWriteOnly) Header() http.Header            { return w.recorder.Header() }
func (w *responseWriteOnly) WriteHeader(status int)         { w.recorder.WriteHeader(status) }
func (w *responseWriteOnly) Write(data []byte) (int, error) { return w.recorder.Write(data) }

type responseFlushAction struct {
	*httptest.ResponseRecorder
	duringFlush func()
}

func (w *responseFlushAction) FlushError() error {
	w.ResponseRecorder.Flush()
	if w.duringFlush != nil {
		w.duringFlush()
	}
	return nil
}

func TestLogicalMailResponseRequiresLiveSuccessfulFlush(t *testing.T) {
	for _, mode := range []string{"no_flush", "unsupported_flush", "cancel_during_flush", "cancel_after_complete_flush"} {
		t.Run(mode, func(t *testing.T) {
			f := newDeliveryFixture(t, "receipt only at the complete write and flush boundary")
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			request, result, _ := responsePreparedMail(t, f, ctx)
			output := httptest.NewRecorder()
			var sink http.ResponseWriter = output
			switch mode {
			case "unsupported_flush":
				sink = &responseWriteOnly{recorder: output}
			case "cancel_during_flush":
				// Cancellation happens before FlushError returns, even though
				// the underlying flush itself reports success.
				sink = &responseFlushAction{ResponseRecorder: output, duringFlush: cancel}
			}
			writer := &logicalMailResponseWriter{
				ResponseWriter: sink, ctx: ctx, request: request, rpcID: json.RawMessage("\"flush-boundary\""),
			}
			event := responseEvent(t, json.RawMessage("\"flush-boundary\""), result, nil)
			responseWriteEvent(t, writer, event)
			if mode != "no_flush" {
				err := writer.FlushError()
				if mode == "unsupported_flush" {
					if !errors.Is(err, http.ErrNotSupported) {
						t.Fatalf("unsupported writer flush returned %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
			// No receipt may be sent from Write or Flush, while the SDK still
			// owns the response stream.
			responseRequireState(t, f, false)
			if mode == "cancel_after_complete_flush" {
				cancel()
			}
			writer.complete(f.server.ctx)
			if !bytes.Equal(output.Body.Bytes(), event) {
				t.Fatal("flush outcome changed the response bytes")
			}
			responseRequireState(t, f, mode == "cancel_after_complete_flush")
		})
	}
}

func TestLogicalMailResponseNumericIDUsesActualSDK(t *testing.T) {
	f := newDeliveryFixture(t, "numeric request normalization retains this original")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	request, _ := f.newRequest(ctx)
	body, err := io.ReadAll(request.Body)
	_ = request.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(body, &input); err != nil {
		t.Fatal(err)
	}
	input["id"] = json.RawMessage("17.0")
	body, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request.Body, request.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	output := httptest.NewRecorder()
	f.server.ServeHTTP(output, request)
	if output.Code != http.StatusOK || !strings.HasPrefix(output.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("numeric SDK response status=%d content-type=%q", output.Code, output.Header().Get("Content-Type"))
	}
	var result map[string]any
	events := bytes.Split(output.Body.Bytes(), []byte("\n\n"))
	for _, event := range events[:len(events)-1] {
		for _, line := range bytes.Split(event, []byte("\n")) {
			payload, ok := bytes.CutPrefix(line, []byte("data:"))
			if !ok {
				continue
			}
			var frame map[string]json.RawMessage
			if json.Unmarshal(bytes.TrimSpace(payload), &frame) != nil || !bytes.Equal(bytes.TrimSpace(frame["id"]), []byte("17")) {
				continue
			}
			if len(frame["error"]) != 0 {
				t.Fatalf("numeric SDK call returned a protocol error: %s", frame["error"])
			}
			if err := json.Unmarshal(frame["result"], &result); err != nil {
				t.Fatal(err)
			}
		}
	}
	if result == nil {
		t.Fatal("SDK did not emit a complete result with its normalized numeric ID")
	}
	payload, _ := deliveryPayload(t, result, f.ordinary)
	deliveryRequireOriginals(t, payload, f.mail.originals...)
	responseRequireState(t, f, true)
}

func TestLogicalMailResponseExactEncodedTextBoundary(t *testing.T) {
	// Measure one actual complete envelope, then change only its ASCII body
	// length. Both body-byte counts remain five digits, so each added byte
	// increases the complete attached text by exactly one byte.
	const seedBodyBytes = 10000
	seed := newDeliveryFixture(t, strings.Repeat("x", seedBodyBytes))
	_, _, seedText := responsePreparedMail(t, seed, context.Background())
	padding := deliveryTextBudget - len(seedText)
	if padding <= 0 || seedBodyBytes+padding+1 >= 100000 {
		t.Fatalf("fixture cannot establish a stable exact envelope boundary: seed bytes=%d padding=%d", len(seedText), padding)
	}
	for _, test := range []struct {
		name  string
		extra int
	}{
		{name: "exact_16KiB"},
		{name: "one_byte_over", extra: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDeliveryFixture(t, strings.Repeat("x", seedBodyBytes+padding+test.extra))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			t.Cleanup(cancel)
			request, id := f.newRequest(ctx)
			output := httptest.NewRecorder()
			f.server.ServeHTTP(output, request)
			result, ok := deliveryResult(output.Body.Bytes(), id)
			if output.Code != http.StatusOK || !ok {
				t.Fatalf("boundary response status=%d complete=%v", output.Code, ok)
			}
			payload, text := deliveryPayload(t, result, f.ordinary)
			if test.extra == 0 {
				if len(text) != deliveryTextBudget {
					t.Fatalf("exact fitting envelope bytes=%d, want %d", len(text), deliveryTextBudget)
				}
				deliveryRequireOriginals(t, payload, f.mail.originals...)
				responseRequireState(t, f, true)
				return
			}
			deliveryRequireOriginals(t, payload)
			fetch, ok := payload["fetch"].(map[string]any)
			if !ok || fetch["tool"] != "agentmail_read" || payload["more"] != true {
				t.Fatal("one-byte oversize original did not retain a usable exact-message fetch notice")
			}
			arguments, ok := fetch["arguments"].(map[string]any)
			if !ok || arguments["id"] != f.mail.originals[0].id || arguments["logical_recipient"] != deliveryLogicalRecipient {
				t.Fatal("oversize fetch lost the original ID or explicit logical recipient")
			}
			if notice, ok := payload["notice"].(string); !ok || notice == "" {
				t.Fatal("oversize original omitted its explanation")
			}
			responseRequireState(t, f, false)
		})
	}
}
