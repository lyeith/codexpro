package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// Delivery retains only the bounded, actually attached originals and the
// current authenticated request's backend binding. It is never durable state.
type logicalMailDelivery struct {
	socket, authorization, credential string
	address, label, text              string
	originals                         []logicalMailOriginal
}

type logicalMailOriginal struct {
	Seq              int64  `json:"seq"`
	ID               string `json:"id"`
	To               string `json:"to"`
	LogicalSender    string `json:"logical_sender"`
	LogicalRecipient string `json:"logical_recipient"`
	Body             string `json:"body"`
}

func logicalMailClient(socket string) (*http.Client, *http.Transport) {
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
		MaxResponseHeaderBytes: 16 << 10,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, transport
}

func encodeLogicalMailText(value any) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(value) != nil || len(logicalMailOfferPrefix)+encoded.Len() > logicalMailMaxTextBytes {
		return ""
	}
	return logicalMailOfferPrefix + encoded.String()
}

func appendLogicalMailText(result wire.Result, text string) wire.Result {
	if text == "" {
		return result
	}
	// Do not append into a caller-owned slice's spare backing capacity.
	content := make([]wire.Content, len(result.Content), len(result.Content)+1)
	copy(content, result.Content)
	result.Content = append(content, wire.Content{"type": "text", "text": text})
	return result
}

func logicalMailBatch(offer logicalMailOffer) (string, []logicalMailOriginal) {
	count := min(logicalMailBatchMessages, len(offer.Messages))
	originals := make([]logicalMailOriginal, count)
	for i := range originals {
		if json.Unmarshal(offer.Messages[i], &originals[i]) != nil {
			return "", nil
		}
	}
	for ; count > 0; count-- {
		selected := offer
		selected.Messages = offer.Messages[:count]
		next := originals[count-1].Seq
		more := count < len(offer.Messages) || *offer.More
		bodyBytes := 0
		for _, original := range originals[:count] {
			bodyBytes += len(original.Body)
		}
		selected.NextAfter, selected.More, selected.BodyBytes = &next, &more, &bodyBytes
		attachment := struct {
			logicalMailOffer
			Notice string `json:"notice,omitempty"`
		}{logicalMailOffer: selected}
		if more {
			attachment.Notice = "More pending originals remain for a later tool response or an existing Agentmail fetch."
		}
		if text := encodeLogicalMailText(attachment); text != "" {
			return text, originals[:count]
		}
	}
	return logicalMailFetchNotice(offer.Mailbox.Address, offer.LogicalRecipient, originals[0].ID), nil
}

// A fetch notice has no delivery plan. Even a successful outer response cannot
// receipt a body that was omitted, including an oversized first original.
func logicalMailFetchNotice(address, label, id string) string {
	arguments := map[string]any{
		"to": address, "logical_recipient": label,
		"pending": true, "inbox": true, "after": 0, "limit": 1,
	}
	fetch := map[string]any{"tool": "agentmail_list", "arguments": arguments}
	notice := map[string]any{
		"mailbox":           map[string]any{"address": address},
		"logical_recipient": label, "messages": []json.RawMessage{}, "more": true,
		"notice": "The first pending original exceeds this attachment. Its body was omitted and remains pending. Fetch it with the existing Agentmail tool below.",
		"fetch":  fetch,
	}
	if id != "" {
		fetch["tool"] = "agentmail_read"
		fetch["arguments"] = map[string]any{"id": id, "logical_recipient": label}
		if text := encodeLogicalMailText(notice); text != "" {
			return text
		}
		// An unusually large ID must not overflow the notice itself.
		fetch["tool"], fetch["arguments"] = "agentmail_list", arguments
	}
	if text := encodeLogicalMailText(notice); text != "" {
		return text
	}
	// Agentmail defaults to the current authenticated mailbox. Do not echo an
	// unbounded configured address if even the scoped list notice cannot fit.
	delete(notice, "mailbox")
	delete(arguments, "to")
	return encodeLogicalMailText(notice)
}

// receive uses Agentmail's existing idempotent read receipt, once per included
// original. One timeout covers the whole batch. An uncertain receipt stops the
// walk; later offers recover originals still pending without changing output.
func (d *logicalMailDelivery) receive(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, logicalMailTimeout)
	defer cancel()
	client, transport := logicalMailClient(d.socket)
	defer transport.CloseIdleConnections()
	for _, original := range d.originals {
		if ctx.Err() != nil {
			return false
		}
		body, _ := json.Marshal(map[string]string{"recipient": d.address, "logical_recipient": d.label})
		endpoint := "http://agentmail.local/v1/messages/" + url.PathEscape(original.ID) + "/read"
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return false
		}
		request.Header.Set("Authorization", d.authorization)
		request.Header.Set("X-Agentmail-Expected-Credential", d.credential)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return false
		}
		if response.StatusCode != http.StatusOK || response.ContentLength > logicalMailMaxWireBytes {
			_ = response.Body.Close()
			return false
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, logicalMailMaxWireBytes+1))
		_ = response.Body.Close()
		if err != nil || len(raw) > logicalMailMaxWireBytes || ctx.Err() != nil ||
			!validLogicalMailReceipt(raw, original) {
			return false
		}
	}
	return true
}

func validLogicalMailReceipt(raw []byte, original logicalMailOriginal) bool {
	var receipt struct {
		ID               string     `json:"id"`
		To               string     `json:"to"`
		LogicalSender    string     `json:"logical_sender"`
		LogicalRecipient string     `json:"logical_recipient"`
		ReadAt           *time.Time `json:"read_at"`
		AckAt            *time.Time `json:"ack_at"`
	}
	return strictLogicalMailJSON(raw) && json.Unmarshal(raw, &receipt) == nil &&
		receipt.ID == original.ID && receipt.To == original.To &&
		receipt.LogicalSender == original.LogicalSender && receipt.LogicalRecipient == original.LogicalRecipient &&
		receipt.ReadAt != nil && receipt.AckAt != nil
}
