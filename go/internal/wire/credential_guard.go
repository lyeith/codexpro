package wire

import (
	"encoding/json"
	"encoding/pem"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// The write guard uses these fixed names in errors and retained audit metadata.
// Names never contain an identifier, literal, or excerpt from submitted content.
var secretRuleNames = []string{
	"known_key",
	"known_provider_token",
	"authorization_bearer",
	"credential_cli_argument",
	"credential_query_parameter",
	"codexpro_token_assignment",
	"codexpro_token_property",
	"credential_named_assignment",
	"credential_named_assignment",
}

func ValidSecretRuleName(name string) bool {
	for _, known := range secretRuleNames {
		if name == known {
			return true
		}
	}
	return false
}

type secretFinding struct {
	value string
	rule  string
}

// Output redaction keeps its existing regexes. Only the broad credential-named
// write rule requires evidence from the assigned literal instead of its name.
var credentialAssignmentPrefix = regexp.MustCompile("(?i)\\b[A-Za-z0-9_]{0,64}(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|PRIVATE[_-]?KEY)[A-Za-z0-9_]{0,64}[\"']?\\s*(?:=|:)\\s*")
var bearerLiteral = regexp.MustCompile("(?i)^Bearer\\s+[A-Za-z0-9._~+/=-]{12,}$")
var connectionPassword = regexp.MustCompile("(?i)(?:^|;)\\s*(?:password|pwd)\\s*=\\s*[^;\\s]+")
var connectionContext = regexp.MustCompile("(?i)(?:^|;)\\s*(?:server|host|data source|database|driver|user id|uid)\\s*=")
var bareMemberReference = regexp.MustCompile("^[A-Za-z_][A-Za-z0-9_]*(?:\\.[A-Za-z_][A-Za-z0-9_]*)+$")

func secretFindings(text string) []secretFinding {
	var findings []secretFinding
	// Preserve all seven specific rules, including their placeholder behavior.
	for i, pattern := range secretPatterns[:7] {
		for _, match := range pattern.FindAllString(text, -1) {
			if !placeholderSecret(match) {
				findings = append(findings, secretFinding{match, secretRuleNames[i]})
			}
		}
	}
	for _, span := range credentialAssignmentPrefix.FindAllStringIndex(text, -1) {
		if end, found := credentialLiteralEnd(text, span[0], span[1]); found {
			findings = append(findings, secretFinding{text[span[0]:end], "credential_named_assignment"})
		}
	}
	return findings
}

// Bound parsing to the literal. This handles raw/triple-quoted PEM blocks as
// well as ordinary one-line assignments without interpreting runtime code.
func assignedLiteral(text string, start int) (string, int, bool, bool) {
	if start >= len(text) {
		return "", start, false, false
	}
	limit := min(len(text), start+16384)
	quote := text[start]
	if quote == '"' || quote == '\'' || quote == 0x60 {
		delim := string(quote)
		if quote != 0x60 && start+3 <= len(text) && text[start:start+3] == strings.Repeat(delim, 3) {
			delim = strings.Repeat(delim, 3)
		}
		begin := start + len(delim)
		for at := begin; at+len(delim) <= limit; at++ {
			if quote != 0x60 && len(delim) == 1 && text[at] == '\\' {
				at++
				continue
			}
			if strings.HasPrefix(text[at:], delim) {
				value := text[begin:at]
				if quote != 0x60 && len(delim) == 1 {
					value = decodeAssignedLiteral(value, quote)
				}
				return value, at + len(delim), true, true
			}
		}
		return "", start, true, false
	}
	// Bare configuration values are allowed, but alphabetic runtime identifiers
	// alone cannot provide high-entropy evidence.
	end := start
	for end < limit && end-start < 512 {
		c := text[end]
		if c <= ' ' || strings.ContainsRune("\"'\x60(),{}[];", rune(c)) {
			break
		}
		end++
	}
	if end == start {
		return "", start, false, false
	}
	return text[start:end], end, false, true
}

func bareLiteralContext(text string, prefixStart, valueStart, valueEnd int) bool {
	lineStart := strings.LastIndexByte(text[:prefixStart], '\n') + 1
	prefix := strings.TrimSpace(text[lineStart:prefixStart])
	if strings.HasSuffix(prefix, "\"") || strings.HasSuffix(prefix, "'") {
		quote := prefix[len(prefix)-1:]
		key := strings.TrimSpace(text[prefixStart:valueStart])
		if strings.HasSuffix(key, ":") && strings.HasSuffix(strings.TrimSpace(strings.TrimSuffix(key, ":")), quote) {
			prefix = strings.TrimSpace(strings.TrimSuffix(prefix, quote))
		}
	}
	if prefix != "" && prefix != "export" && prefix != "-" {
		// A declaration, member expression, or parameter is source syntax,
		// not evidence that the following identifier is a literal value.
		return false
	}
	if bareMemberReference.MatchString(text[valueStart:valueEnd]) {
		return false
	}
	tail := strings.TrimLeft(text[valueEnd:], " \t")
	return tail == "" || !strings.ContainsRune("(.<[=:?;+-*/&|", rune(tail[0]))
}

func credentialLiteralEnd(text string, prefixStart, start int) (int, bool) {
	value, end, quoted, ok := assignedLiteral(text, start)
	if ok && (quoted || bareLiteralContext(text, prefixStart, start, end)) &&
		!placeholderSecret(value) && credentialLiteralEvidence(value, quoted) {
		return end, true
	}
	// Shell and TOML single-quoted literals do not interpret backslashes.
	// Check that bounded interpretation separately from an escaped string.
	if start < len(text) && text[start] == '\'' {
		limit := min(len(text), start+16384)
		if offset := strings.IndexByte(text[start+1:limit], '\''); offset >= 0 {
			end := start + 1 + offset
			value := text[start+1 : end]
			if !placeholderSecret(value) && credentialLiteralEvidence(value, true) {
				return end + 1, true
			}
		}
	}
	return start, false
}

func decodeAssignedLiteral(value string, quote byte) string {
	if quote == '"' {
		var decoded string
		if json.Unmarshal([]byte("\""+value+"\""), &decoded) == nil {
			return decoded
		}
	}
	original := value
	var decoded strings.Builder
	for value != "" {
		r, _, remaining, err := strconv.UnquoteChar(value, quote)
		if err != nil {
			return original
		}
		decoded.WriteRune(r)
		value = remaining
	}
	return decoded.String()
}

func credentialLiteralEvidence(value string, quoted bool) bool {
	value = strings.TrimSpace(value)
	for _, index := range []int{0, 1, 2} {
		if secretPatterns[index].MatchString(value) {
			return true
		}
	}
	if bearerLiteral.MatchString(value) {
		return true
	}
	// Recognize a complete PEM envelope, including escaped newlines in a string.
	pemText := strings.ReplaceAll(strings.ReplaceAll(value, "\\r\\n", "\n"), "\\n", "\n")
	if block, _ := pem.Decode([]byte(pemText)); block != nil &&
		strings.HasSuffix(block.Type, "PRIVATE KEY") && len(block.Bytes) > 0 {
		return true
	}
	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" &&
			parsed.Host != "" && parsed.User != nil {
			if password, present := parsed.User.Password(); present && password != "" {
				return true
			}
		}
		// An ordinary URL is not entropy evidence; its credential-bearing
		// query parameters remain covered by their unchanged specific rule.
		return false
	}
	if connectionPassword.MatchString(value) && connectionContext.MatchString(value) {
		return true
	}
	return highEntropyLiteral(value, quoted)
}

func highEntropyLiteral(value string, quoted bool) bool {
	minimum := 20
	if quoted {
		minimum = 12
	}
	if len(value) < minimum || len(value) > 512 {
		return false
	}
	counts := map[rune]int{}
	digit, letter, hex := false, false, true
	for _, r := range value {
		if r > unicode.MaxASCII || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
		counts[r]++
		digit = digit || r >= '0' && r <= '9'
		letter = letter || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		hex = hex && (r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F')
	}
	if !quoted && (!digit || !letter) {
		return false
	}
	// Preserve the existing exemption for digit-free paths and textual labels.
	// Known credential formats have already been checked independently above.
	if !digit && strings.ContainsAny(value, "./:_- "+"\t\r\n") {
		return false
	}
	entropy := 0.0
	for _, count := range counts {
		p := float64(count) / float64(len(value))
		entropy -= p * math.Log2(p)
	}
	if hex {
		return len(value) >= 32 && len(counts) >= 10 && entropy >= 3.25
	}
	return len(counts) >= 12 && entropy >= 4.0
}

// SecretRuleNames returns only fixed identifiers suitable for public metadata.
func SecretRuleNames(text string, limit int) []string {
	if limit <= 0 {
		limit = 3
	}
	names := []string{}
	seen := map[string]bool{}
	for _, finding := range secretFindings(text) {
		if !seen[finding.rule] {
			names = append(names, finding.rule)
			seen[finding.rule] = true
			if len(names) >= limit {
				break
			}
		}
	}
	return names
}
