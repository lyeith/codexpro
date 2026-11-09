package config

import "errors"

// Logical mail is an opt-in local integration, selected only by trusted startup
// settings. The request's caller label never selects a principal or mailbox.
func (c *Config) validateLogicalMail() error {
	configured := 0
	for _, key := range []string{"logicalMailSocket", "logicalMailPrincipal", "logicalMailAgent", "logicalMailMailbox"} {
		if c.String(key, "") != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil
	}
	if configured != 4 {
		return errors.New("logical mail requires socket, principal, agent and mailbox together")
	}
	if c.String("principalsFile", "") == "" {
		return errors.New("logical mail requires named authentication")
	}
	return nil
}
