package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const logicalMailLabelHeader = "X-Codexpro-Go-Logical-Mail-Label"

const logicalMailContextHeader = "X-Codexpro-Go-Logical-Mail-Request"

// The opaque handle joins only an optional offer to its current HTTP request.
// It is never a session, caller identity, routing label or durable receipt.
func (s *Server) trackLogicalMailRequest(ctx context.Context) (string, func()) {
	lifetime, cancel := context.WithCancel(ctx)
	handle := uuid()
	s.mu.Lock()
	if s.mailRequests == nil {
		s.mailRequests = make(map[string]context.Context)
	}
	s.mailRequests[handle] = lifetime
	s.mu.Unlock()
	return handle, func() {
		cancel()
		s.mu.Lock()
		delete(s.mailRequests, handle)
		s.mu.Unlock()
	}
}

func (s *Server) attachLogicalMailForRequest(ctx context.Context, header http.Header, id wire.Identity, args map[string]any, result wire.Result) wire.Result {
	s.mu.Lock()
	lifetime := s.mailRequests[header.Get(logicalMailContextHeader)]
	s.mu.Unlock()
	if lifetime == nil || lifetime.Err() != nil {
		return result
	}
	offerCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetime, cancel)
	defer func() { stop(); cancel() }()
	if lifetime.Err() != nil {
		cancel()
	}
	return s.attachLogicalMail(offerCtx, header.Get("Authorization"), id, logicalMailRequestArguments(header, args), result)
}

// The SDK decodes objects before invoking a tool. Inspect the original request
// first so duplicate routing fields cannot silently select the final label.
// This is a mail-selection guard only: the ordinary SDK result is preserved.
func logicalMailRequestLabel(raw []byte) string {
	method, ok := uniqueMailJSONField(raw, "method")
	var methodName string
	if !ok || json.Unmarshal(method, &methodName) != nil || methodName != "tools/call" {
		return ""
	}
	params, ok := uniqueMailJSONField(raw, "params")
	if !ok {
		return ""
	}
	arguments, ok := uniqueMailJSONField(params, "arguments")
	if !ok {
		return ""
	}
	value, ok := uniqueMailJSONField(arguments, "agent")
	if !ok {
		return ""
	}
	var label string
	if json.Unmarshal(value, &label) != nil || !wire.ValidAgent(label) {
		return ""
	}
	return label
}

func uniqueMailJSONField(raw []byte, field string) (json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	seen := make(map[string]bool)
	var selected json.RawMessage
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		folded := strings.ToLower(key)
		if err != nil || !ok || seen[folded] {
			return nil, false
		}
		seen[folded] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		if key == field {
			selected = value
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	return selected, selected != nil
}

func logicalMailRequestArguments(header http.Header, args map[string]any) map[string]any {
	label, _ := args["agent"].(string)
	if label == "" || label != header.Get(logicalMailLabelHeader) {
		return nil
	}
	return args
}
