package config

import (
	"os"
	"strings"
	"testing"
)

func TestPrivateTokenPersistsAndRequiresPrivatePermissions(t *testing.T) {
	c := New(t.TempDir())
	a, e := c.Token()
	if e != nil {
		t.Fatal(e)
	}
	b, e := c.Token()
	if e != nil {
		t.Fatal(e)
	}
	if a != b || len(a) < 48 || strings.ContainsAny(a, " \t\r\n") {
		t.Fatal("unstable or malformed private credential")
	}
	if e := os.Chmod(c.TokenFile, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Token(); e == nil {
		t.Fatal("world-readable credential accepted")
	}
}
func TestReloadIsAtomic(t *testing.T) {
	c := New(t.TempDir())
	if e := os.WriteFile(c.EnvFile, []byte("CODEXPRO_MAX_JOBS=3\nCODEXPRO_MAX_OUTPUT_BYTES=oops\n"), 0600); e != nil {
		t.Fatal(e)
	}
	before := c.Int("maxJobs", 0)
	if e := c.Reload(); e == nil {
		t.Fatal("invalid limit accepted")
	}
	if c.Int("maxJobs", 0) != before {
		t.Fatal("partial update escaped rejected reload")
	}
}
