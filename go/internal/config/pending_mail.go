package config

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// PendingMailBinding is trusted startup routing, never request-supplied authority.
// An empty logical recipient explicitly selects ordinary canonical mailbox mail.
type PendingMailBinding struct {
	Label            string
	Principal        string
	Agent            string
	Mailbox          string
	LogicalRecipient string
}

var pendingMailLabel = regexp.MustCompile(`^(SP|AP|RDP)[1-9][0-9]*$`)
var pendingMailID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var pendingMailAddress = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)*$`)

func ValidPendingMailAddress(value string) bool {
	return len(value) <= 256 && pendingMailAddress.MatchString(value)
}

// ParsePendingMailBindings rejects missing, duplicate and unknown routing fields.
// The bounded grammar has only an array of objects with exact string keys/values.
func ParsePendingMailBindings(raw string) (map[string]PendingMailBinding, error) {
	invalid := errors.New("pending mail requires explicit, distinct named bindings")
	if len(raw) == 0 || len(raw) > 16<<10 {
		return nil, invalid
	}
	d := json.NewDecoder(strings.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('[') {
		return nil, invalid
	}
	bindings := make(map[string]PendingMailBinding)
	for d.More() {
		if len(bindings) == 64 {
			return nil, invalid
		}
		token, err = d.Token()
		if err != nil || token != json.Delim('{') {
			return nil, invalid
		}
		fields := map[string]string{}
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok {
				return nil, invalid
			}
			switch name {
			case "label", "principal", "agent", "mailbox", "logical_recipient":
			default:
				return nil, invalid
			}
			if _, exists := fields[name]; exists {
				return nil, invalid
			}
			item, err := d.Token()
			value, ok := item.(string)
			if err != nil || !ok {
				return nil, invalid
			}
			fields[name] = value
		}
		token, err = d.Token()
		if err != nil || token != json.Delim('}') || len(fields) != 5 {
			return nil, invalid
		}
		b := PendingMailBinding{Label: fields["label"], Principal: fields["principal"],
			Agent: fields["agent"], Mailbox: fields["mailbox"], LogicalRecipient: fields["logical_recipient"]}
		if !wire.ValidAgent(b.Label) || !pendingMailLabel.MatchString(b.Label) ||
			!pendingMailID.MatchString(b.Principal) || !pendingMailID.MatchString(b.Agent) ||
			!ValidPendingMailAddress(b.Mailbox) || (b.LogicalRecipient != "" && !wire.ValidAgent(b.LogicalRecipient)) {
			return nil, invalid
		}
		if _, exists := bindings[b.Label]; exists {
			return nil, invalid
		}
		bindings[b.Label] = b
	}
	token, err = d.Token()
	if err != nil || token != json.Delim(']') || len(bindings) == 0 {
		return nil, invalid
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, invalid
	}
	return bindings, nil
}

func (c *Config) validatePendingMail() error {
	socket, bindings := c.String("pendingMailSocket", ""), c.String("pendingMailBindings", "")
	if socket == "" && bindings == "" {
		return nil
	}
	if socket == "" || bindings == "" || c.String("principalsFile", "") == "" {
		return errors.New("pending mail requires a local socket, explicit bindings and named authentication")
	}
	_, err := ParsePendingMailBindings(bindings)
	return err
}
