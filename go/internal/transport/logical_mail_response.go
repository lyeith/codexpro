package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// The pinned SDK writes one complete SSE event with a compact JSON data line,
// then Flushes it. Its writeEvent discards the flush error, so neither HTTP 200
// nor handler completion is sufficient evidence. This observer forwards output
// unchanged and makes no backend calls while the SDK holds its stream.
type logicalMailResponseWriter struct {
	http.ResponseWriter
	ctx     context.Context
	request *logicalMailRequest
	rpcID   json.RawMessage

	mu      sync.Mutex
	failed  bool
	written *logicalMailDelivery
	flushed bool
}

func (w *logicalMailResponseWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if code != http.StatusOK {
		w.failed = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *logicalMailResponseWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ctx.Err() != nil {
		w.failed = true
	}
	n, err := w.ResponseWriter.Write(data)
	if err != nil || n != len(data) || w.ctx.Err() != nil {
		w.failed = true
	}
	if !w.failed && w.written == nil {
		if delivery := w.request.preparedDelivery(); delivery != nil && logicalMailResultEvent(data, w.rpcID, delivery.text) {
			w.written = delivery
		}
	}
	return n, err
}

func (w *logicalMailResponseWriter) FlushError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err != nil || w.ctx.Err() != nil {
		w.failed = true
	}
	if w.written != nil && !w.failed {
		w.flushed = true
	}
	return err
}

func (w *logicalMailResponseWriter) Flush()                      { _ = w.FlushError() }
func (w *logicalMailResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// complete reports an unconfirmed receipt for a successfully delivered response.
func (w *logicalMailResponseWriter) complete(ctx context.Context) bool {
	w.mu.Lock()
	delivery := w.written
	delivered := delivery != nil && w.flushed && !w.failed
	w.mu.Unlock()
	if !delivered {
		return false
	}
	// Request cancellation after the confirmed full write/flush does not undo
	// that boundary. Use server lifetime and one bounded receipt budget, not
	// the already-completed offer context or a new asynchronous retry loop.
	return !delivery.receive(ctx)
}

func logicalMailResultEvent(data []byte, requestID json.RawMessage, text string) bool {
	if !bytes.HasSuffix(data, []byte("\n\n")) {
		return false
	}
	events := bytes.Split(data, []byte("\n\n"))
	for _, event := range events[:len(events)-1] {
		message := false
		var payload []byte
		for _, line := range bytes.Split(event, []byte("\n")) {
			if bytes.Equal(line, []byte("event: message")) {
				message = true
			}
			if value, ok := bytes.CutPrefix(line, []byte("data:")); ok {
				if payload != nil {
					// A changed SDK framing contract requires qualification;
					// uncertain framing must leave originals pending.
					message = false
					break
				}
				payload = bytes.TrimPrefix(value, []byte(" "))
			}
		}
		if !message {
			continue
		}
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Error  json.RawMessage `json:"error"`
			Result *struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if json.Unmarshal(payload, &frame) != nil || len(frame.Error) != 0 ||
			frame.Result == nil || !sameLogicalMailRPCID(requestID, frame.ID) {
			continue
		}
		content := frame.Result.Content
		if len(content) > 0 && content[len(content)-1].Type == "text" && content[len(content)-1].Text == text {
			return true
		}
	}
	return false
}

// Match the original typed ID, never its lossy access-log/header rendering.
// Use the pinned SDK's own ID conversion, including its numeric normalization;
// an integer-looking string must still differ from a numeric request ID.
func sameLogicalMailRPCID(request, response json.RawMessage) bool {
	var a, b any
	if json.Unmarshal(request, &a) != nil || json.Unmarshal(response, &b) != nil {
		return false
	}
	requestID, requestErr := jsonrpc.MakeID(a)
	responseID, responseErr := jsonrpc.MakeID(b)
	return requestErr == nil && responseErr == nil && requestID.IsValid() && requestID == responseID
}
