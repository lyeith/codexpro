//go:build !unix

package plansaccess

// The policy requires Unix ownership, private modes and no-follow openat.
func Load(string) (*Policy, error) { return nil, errUnavailable }
