package config

import (
	"errors"
	"regexp"
)

var pendingMailAddress = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)*$`)

func ValidPendingMailAddress(value string) bool {
	return len(value) <= 256 && pendingMailAddress.MatchString(value)
}

func (c *Config) validatePendingMail() error {
	if c.String("pendingMailSocket", "") != "" && c.String("principalsFile", "") == "" {
		return errors.New("pending mail requires a local socket and named authentication")
	}
	return nil
}
