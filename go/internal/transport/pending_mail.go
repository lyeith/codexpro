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

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
)

const pendingMailMaxWireBytes = 4 << 10

// Each named tool call asks Agentmail to resolve its current registration.
// CodexPro retains neither label routing nor pending summary state.
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
	lookupCtx, cancel := context.WithTimeout(ctx, logicalMailTimeout)
	defer cancel()
	client, transport := logicalMailClient(socket)
	defer transport.CloseIdleConnections()
	endpoint := url.URL{Scheme: "http", Host: "agentmail.local", Path: "/v1/mailboxes/pending-summary"}
	endpoint.RawQuery = url.Values{"attribution": {label}}.Encode()
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
	line := pendingMailLine(body, label)
	if lookupCtx.Err() != nil {
		return result
	}
	return appendLogicalMailText(result, line)
}

func pendingMailLine(body []byte, label string) string {
	var fields map[string]json.RawMessage
	var summary struct {
		Count        *int64  `json:"count"`
		LatestSender *string `json:"latest_sender"`
	}
	if !wire.ValidAgent(label) || !strictLogicalMailJSON(body) ||
		json.Unmarshal(body, &fields) != nil || len(fields) != 2 ||
		logicalMailFields(body, "count", "latest_sender") == nil ||
		json.Unmarshal(body, &summary) != nil || summary.Count == nil ||
		summary.LatestSender == nil || *summary.Count <= 0 ||
		!config.ValidPendingMailAddress(*summary.LatestSender) {
		return ""
	}
	return fmt.Sprintf("%s: %d unread, latest from %s", label, *summary.Count, *summary.LatestSender)
}
