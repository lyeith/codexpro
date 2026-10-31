package wire

import (
	"strings"
	"unicode"
)

// RE2's (?i) uses Unicode simple folding. Required ASCII words can therefore
// contain non-ASCII members of those cycles, notably long s and Kelvin sign.
// Derive every such alias from the same Unicode table instead of assuming an
// ASCII-only case-insensitive search or weakening a candidate pattern.
var secretASCIIAliases = func() map[rune]rune {
	aliases := map[rune]rune{}
	for lower := 'a'; lower <= 'z'; lower++ {
		for next := unicode.SimpleFold(lower); next != lower; next = unicode.SimpleFold(next) {
			if next > 127 {
				aliases[next] = lower
			}
		}
	}
	return aliases
}()

type secretPatternPrefilter struct {
	text   string
	folded string
	ready  bool
}

func (f *secretPatternPrefilter) fold() string {
	if !f.ready {
		f.folded = strings.Map(func(r rune) rune {
			if r >= 'A' && r <= 'Z' {
				return r + ('a' - 'A')
			}
			if r > 127 {
				if ascii, ok := secretASCIIAliases[r]; ok {
					return ascii
				}
			}
			return r
		}, f.text)
		f.ready = true
	}
	return f.folded
}

// possible rejects a scan only if a literal required by that exact expression
// is absent. It intentionally accepts false positives: boundaries, adjacency,
// quoting, lengths and the actual replacement remain the original regexp's
// job. A changed replacement gets a fresh filter before the next expression.
func (f *secretPatternPrefilter) possible(index int) bool {
	switch index {
	case 0:
		return strings.Contains(f.text, "sk-")
	case 1:
		return strings.Contains(f.text, "sk-ant-") ||
			strings.Contains(f.text, "gh") ||
			strings.Contains(f.text, "github_pat_") ||
			strings.Contains(f.text, "npm_")
	case 2:
		if !strings.Contains(f.text, ":") {
			return false
		}
		folded := f.fold()
		return strings.Contains(folded, "authorization") && strings.Contains(folded, "bearer")
	case 3:
		folded := f.fold()
		return strings.Contains(folded, "ngrok") || strings.Contains(folded, "cloudflared") ||
			strings.Contains(folded, "--") && (strings.Contains(folded, "token") || strings.Contains(folded, "key"))
	case 4:
		if !strings.ContainsAny(f.text, "?&") || !strings.Contains(f.text, "=") {
			return false
		}
		folded := f.fold()
		return strings.Contains(folded, "token") || strings.Contains(folded, "key")
	case 5:
		return strings.Contains(f.text, "=") && strings.Contains(f.fold(), "codexpro_token")
	case 6:
		return strings.Contains(f.text, ":") && strings.Contains(f.fold(), "codexpro_token")
	case 7, 8:
		if index == 7 && !strings.Contains(f.text, "=") || index == 8 && !strings.Contains(f.text, ":") {
			return false
		}
		folded := f.fold()
		for _, marker := range []string{
			"token", "secret", "password",
			"apikey", "api_key", "api-key",
			"privatekey", "private_key", "private-key",
		} {
			if strings.Contains(folded, marker) {
				return true
			}
		}
		return false
	default:
		// Future expressions receive no filtering until their required
		// literals and equivalence tests are explicitly added.
		return true
	}
}
