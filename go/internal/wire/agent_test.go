package wire

import (
	"strings"
	"testing"
)

func TestValidAgent(t *testing.T) {
	for _, label := range []string{"SP-3", "a", "A-01", "SP3", "CO", "SUP", "ABCD", strings.Repeat("Z", AgentMaxLength)} {
		if !ValidAgent(label) {
			t.Errorf("valid label %q refused", label)
		}
	}
	for _, label := range []string{"", "ABCDE", "SP 3", "SP_3", "SP/3", "SP\n3", "SP-3\n", "é", strings.Repeat("a", AgentMaxLength+1)} {
		if ValidAgent(label) {
			t.Errorf("invalid label %q accepted", label)
		}
	}
}
