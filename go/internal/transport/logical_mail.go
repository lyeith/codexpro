package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const (
	logicalMailTimeout       = 2 * time.Second
	logicalMailMaxWireBytes  = 512 << 10
	logicalMailMaxBodyBytes  = 64 << 10
	logicalMailMaxMessages   = 10 // Validate the backend's hard offer limit.
	logicalMailBatchMessages = 3
	logicalMailMaxTextBytes  = 16 << 10
	logicalMailMaxRecipients = 64
	logicalMailOfferPrefix   = "Agentmail logical-mail delivery\nThese complete original messages are receipted after this combined tool response is successfully written. Repeated IDs can occur when a receipt is unconfirmed or responses overlap. Delivery does not establish model consumption. The logical label routes within the authenticated principal; it is not a separate identity or verified conversation.\n\n"
)

type logicalMailOffer struct {
	Mailbox struct {
		Address           string   `json:"address"`
		Revision          int64    `json:"revision"`
		LogicalRecipients []string `json:"logical_recipients"`
	} `json:"mailbox"`
	LogicalRecipient string            `json:"logical_recipient"`
	Messages         []json.RawMessage `json:"messages"`
	NextAfter        *int64            `json:"next_after"`
	More             *bool             `json:"more"`
	BodyBytes        *int              `json:"body_bytes"`
	MaxBodyBytes     *int              `json:"max_body_bytes"`
}

// Attachment alone is not a receipt. Only the request's completed response
// writer can use the prepared delivery after writing and flushing its result.
func (s *Server) attachLogicalMail(ctx context.Context, authorization string, id wire.Identity, args map[string]any, result wire.Result) wire.Result {
	attached, _ := s.prepareLogicalMail(ctx, authorization, id, args, result)
	return attached
}

func (s *Server) prepareLogicalMail(ctx context.Context, authorization string, id wire.Identity, args map[string]any, result wire.Result) (wire.Result, *logicalMailDelivery) {
	if s == nil || s.Config == nil || ctx.Err() != nil {
		return result, nil
	}
	values := s.Config.Snapshot()
	socket := wire.String(values, "logicalMailSocket", "")
	principal := wire.String(values, "logicalMailPrincipal", "")
	agent := wire.String(values, "logicalMailAgent", "")
	address := wire.String(values, "logicalMailMailbox", "")
	label, ok := args["agent"].(string)
	if socket == "" || principal == "" || agent == "" || address == "" ||
		!filepath.IsAbs(socket) || strings.ContainsAny(socket, "\x00\r\n") ||
		id.Source != "named" || id.PrincipalType != "system" || id.CredentialID == "" || id.Restricted() ||
		id.PrincipalID != principal || id.AgentID != agent || !ok || !wire.ValidAgent(label) ||
		strings.TrimSpace(authorization) == "" || strings.ContainsAny(authorization, "\r\n") {
		return result, nil
	}

	offerCtx, cancel := context.WithTimeout(ctx, logicalMailTimeout)
	defer cancel()
	client, transport := logicalMailClient(socket)
	defer transport.CloseIdleConnections()
	endpoint := url.URL{Scheme: "http", Host: "agentmail.local", Path: "/v1/mailboxes/logical-offer"}
	endpoint.RawQuery = url.Values{
		"address": {address}, "logical_recipient": {label}, "after": {"0"}, "limit": {strconv.Itoa(logicalMailBatchMessages)},
	}.Encode()
	request, err := http.NewRequestWithContext(offerCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return result, nil
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("X-Agentmail-Expected-Credential", id.CredentialID)
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return result, nil
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusRequestEntityTooLarge && offerCtx.Err() == nil {
		// The backend's oversize error has no structured original ID. Never
		// extract routing data from its diagnostic body or receipt a notice.
		return appendLogicalMailText(result, logicalMailFetchNotice(address, label, "")), nil
	}
	if response.StatusCode != http.StatusOK || response.ContentLength > logicalMailMaxWireBytes {
		return result, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, logicalMailMaxWireBytes+1))
	if err != nil || len(body) > logicalMailMaxWireBytes || offerCtx.Err() != nil {
		return result, nil
	}
	offer, ok := decodeLogicalMailOffer(body, address, label)
	if !ok || len(offer.Messages) == 0 || offerCtx.Err() != nil {
		return result, nil
	}
	text, originals := logicalMailBatch(offer)
	if text == "" || offerCtx.Err() != nil {
		return result, nil
	}
	result = appendLogicalMailText(result, text)
	if len(originals) == 0 {
		return result, nil
	}
	return result, &logicalMailDelivery{
		socket: socket, authorization: authorization, credential: id.CredentialID,
		address: address, label: label, text: text, originals: originals,
	}
}

func logicalMailFields(raw []byte, names ...string) map[string]json.RawMessage {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil
	}
	for _, name := range names {
		if _, ok := object[name]; !ok {
			return nil
		}
	}
	return object
}

func decodeLogicalMailOffer(body []byte, address, label string) (logicalMailOffer, bool) {
	var offer logicalMailOffer
	if !strictLogicalMailJSON(body) {
		return offer, false
	}
	outer := logicalMailFields(body,
		"mailbox", "logical_recipient", "messages", "next_after", "more", "body_bytes", "max_body_bytes")
	if outer == nil || json.Unmarshal(body, &offer) != nil {
		return offer, false
	}
	if logicalMailFields(outer["mailbox"], "address", "revision", "logical_recipients") == nil ||
		offer.Mailbox.Address != address || offer.Mailbox.Revision < 1 || offer.LogicalRecipient != label ||
		len(offer.Mailbox.LogicalRecipients) < 1 || len(offer.Mailbox.LogicalRecipients) > logicalMailMaxRecipients ||
		offer.Messages == nil || len(offer.Messages) > logicalMailMaxMessages ||
		offer.NextAfter == nil || offer.More == nil || offer.BodyBytes == nil || offer.MaxBodyBytes == nil ||
		*offer.NextAfter < 0 || *offer.MaxBodyBytes < 1 || *offer.MaxBodyBytes > logicalMailMaxBodyBytes ||
		*offer.BodyBytes < 0 || *offer.BodyBytes > *offer.MaxBodyBytes {
		return offer, false
	}
	recipients := make(map[string]bool, len(offer.Mailbox.LogicalRecipients))
	for _, recipient := range offer.Mailbox.LogicalRecipients {
		if !wire.ValidAgent(recipient) || recipients[recipient] {
			return offer, false
		}
		recipients[recipient] = true
	}
	if !recipients[label] {
		return offer, false
	}
	ids := make(map[string]bool, len(offer.Messages))
	var previous int64
	bodyBytes := 0
	for _, raw := range offer.Messages {
		var message struct {
			Seq              int64           `json:"seq"`
			ID               string          `json:"id"`
			From             string          `json:"from"`
			To               string          `json:"to"`
			LogicalRecipient string          `json:"logical_recipient"`
			Body             *string         `json:"body"`
			ReplyTo          json.RawMessage `json:"reply_to"`
		}
		if logicalMailFields(raw, "seq", "id", "from", "to", "logical_recipient", "body") == nil ||
			json.Unmarshal(raw, &message) != nil || message.Seq <= previous ||
			strings.TrimSpace(message.ID) == "" || ids[message.ID] || strings.TrimSpace(message.From) == "" ||
			message.To != address || message.LogicalRecipient != label || message.Body == nil {
			return offer, false
		}
		if len(message.ReplyTo) != 0 {
			var replyTo string
			if bytes.Equal(message.ReplyTo, []byte("null")) || json.Unmarshal(message.ReplyTo, &replyTo) != nil {
				return offer, false
			}
		}
		if len(*message.Body) > *offer.MaxBodyBytes-bodyBytes {
			return offer, false
		}
		bodyBytes += len(*message.Body)
		previous = message.Seq
		ids[message.ID] = true
	}
	if bodyBytes != *offer.BodyBytes || previous != *offer.NextAfter || len(offer.Messages) == 0 && *offer.More {
		return offer, false
	}
	return offer, true
}

// encoding/json permits duplicate keys and repairs invalid UTF-8 or unpaired
// Unicode surrogates. An offer must preserve complete original bodies instead.
func strictLogicalMailJSON(data []byte) bool {
	if !utf8.Valid(data) || !json.Valid(data) {
		return false
	}
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' {
			continue
		}
		unit, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if unit < 0xd800 || unit > 0xdfff {
			continue
		}
		if unit > 0xdbff || i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 64 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delim, container := token.(json.Delim)
		if !container {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				token, err := decoder.Token()
				key, ok := token.(string)
				folded := strings.ToLower(key)
				if err != nil || !ok || seen[folded] {
					return false
				}
				seen[folded] = true
				if !value(depth + 1) {
					return false
				}
			}
		case '[':
			for decoder.More() {
				if !value(depth + 1) {
					return false
				}
			}
		default:
			return false
		}
		_, err = decoder.Token()
		return err == nil
	}
	if !value(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}
