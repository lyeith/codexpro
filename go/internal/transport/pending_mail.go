package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
)

const pendingMailLookupLifetime = 2 * time.Second
const pendingMailMaxWireBytes = 4 << 10

// Cache only parsed trusted routing, never counts, senders or bearer material.
// Each tool call rechecks its named identity and obtains a fresh owner snapshot.
type pendingMailLookup struct {
	mu       sync.Mutex
	raw      string
	until    time.Time
	bindings map[string]config.PendingMailBinding
}

func (cache *pendingMailLookup) binding(raw, label string, id wire.Identity) (config.PendingMailBinding, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := time.Now()
	if raw != cache.raw || !now.Before(cache.until) {
		cache.bindings, _ = config.ParsePendingMailBindings(raw)
		cache.raw, cache.until = raw, now.Add(pendingMailLookupLifetime)
	}
	b, ok := cache.bindings[label]
	return b, ok && b.Principal == id.PrincipalID && b.Agent == id.AgentID
}

func (s *Server) attachPendingMail(ctx context.Context, authorization string, id wire.Identity, args map[string]any, result wire.Result) wire.Result {
	if s == nil || s.Config == nil || ctx.Err() != nil {
		return result
	}
	values := s.Config.Snapshot()
	socket := wire.String(values, "pendingMailSocket", "")
	label, ok := args["agent"].(string)
	if socket == "" || !filepath.IsAbs(socket) || strings.ContainsAny(socket, "\x00\r\n") ||
		id.Source != "named" || id.PrincipalType != "system" || id.CredentialID == "" || id.Restricted() ||
		!ok || !wire.ValidAgent(label) || strings.TrimSpace(authorization) == "" || strings.ContainsAny(authorization, "\r\n") {
		return result
	}
	binding, ok := s.pendingMail.binding(wire.String(values, "pendingMailBindings", ""), label, id)
	if !ok {
		return result
	}
	lookupCtx, cancel := context.WithTimeout(ctx, logicalMailTimeout)
	defer cancel()
	client, transport := logicalMailClient(socket)
	defer transport.CloseIdleConnections()
	endpoint := url.URL{Scheme: "http", Host: "agentmail.local", Path: "/v1/mailboxes/pending-summary"}
	endpoint.RawQuery = url.Values{
		"address": {binding.Mailbox}, "logical_recipient": {binding.LogicalRecipient},
	}.Encode()
	request, err := http.NewRequestWithContext(lookupCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return result
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("X-Agentmail-Expected-Credential", id.CredentialID)
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return result
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > pendingMailMaxWireBytes {
		return result
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, pendingMailMaxWireBytes+1))
	if err != nil || len(body) > pendingMailMaxWireBytes || lookupCtx.Err() != nil {
		return result
	}
	line := pendingMailLine(body, binding)
	if lookupCtx.Err() != nil {
		return result
	}
	return appendLogicalMailText(result, line)
}

func pendingMailLine(body []byte, binding config.PendingMailBinding) string {
	var fields map[string]json.RawMessage
	var summary struct {
		Address          *string `json:"address"`
		LogicalRecipient *string `json:"logical_recipient"`
		Count            *int64  `json:"count"`
		LatestSender     *string `json:"latest_sender"`
	}
	if !strictLogicalMailJSON(body) || json.Unmarshal(body, &fields) != nil || len(fields) != 4 ||
		!logicalMailFields(body, "address", "logical_recipient", "count", "latest_sender") ||
		json.Unmarshal(body, &summary) != nil || summary.Address == nil || summary.LogicalRecipient == nil ||
		summary.Count == nil || summary.LatestSender == nil || *summary.Address != binding.Mailbox ||
		*summary.LogicalRecipient != binding.LogicalRecipient || *summary.Count <= 0 ||
		!config.ValidPendingMailAddress(*summary.LatestSender) {
		return ""
	}
	return fmt.Sprintf("%s: %d unread, latest from %s", binding.Label, *summary.Count, *summary.LatestSender)
}
