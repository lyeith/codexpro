package wire

import (
	"regexp"
	"strings"
	"unicode"
)

const redactedSecret = "[REDACTED_SECRET]"

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile("\\bsk-[A-Za-z0-9_-]{10,}\\b"),
	regexp.MustCompile("\\b(?:sk-ant-[A-Za-z0-9_-]{10,}|gh[opsru]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|npm_[A-Za-z0-9_-]{20,})\\b"),
	regexp.MustCompile("(?i)\\b(Authorization\\s*:\\s*Bearer\\s+)[A-Za-z0-9._~+/=-]{12,}"),
	regexp.MustCompile("(?i)((?:\\bngrok\\s+config\\s+add-authtoken|\\bcloudflared\\s+service\\s+install|--(?:token|access-token|auth-token|api[_-]?key|authtoken))(?:=|\\s+))[A-Za-z0-9._~+/=-]{8,}"),
	regexp.MustCompile("(?i)([?&](?:codexpro_token|token|access_token|auth_token|api[_-]?key)=)[^&\\s\"'`<>]{8,}"),
	regexp.MustCompile("(?i)\\b(codexpro_token\\s*=\\s*)(?:\"[^\"\\r\\n]{8,512}\"|'[^'\\r\\n]{8,512}'|`[^`\\r\\n]{8,512}`|[A-Za-z0-9_./+=-]{8,512})"),
	regexp.MustCompile("(?i)([\"']?codexpro_token[\"']?\\s*:\\s*)(?:\"[^\"\\r\\n]{8,512}\"|'[^'\\r\\n]{8,512}'|`[^`\\r\\n]{8,512}`|[A-Za-z0-9_./+=-]{8,512})"),
	regexp.MustCompile("(?i)\\b[A-Za-z0-9_]{0,64}(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|PRIVATE[_-]?KEY)[A-Za-z0-9_]{0,64}\\s*=\\s*(?:\"[^\"\\r\\n]{12,512}\"|'[^'\\r\\n]{12,512}'|`[^`\\r\\n]{12,512}`|[A-Za-z0-9_./+=-]{20,512})"),
	regexp.MustCompile("(?i)([\"']?[A-Za-z0-9_]{0,64}(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|PRIVATE[_-]?KEY)[A-Za-z0-9_]{0,64}[\"']?\\s*:\\s*)(?:\"[^\"\\r\\n]{12,512}\"|'[^'\\r\\n]{12,512}'|`[^`\\r\\n]{12,512}`|[A-Za-z0-9_./+=-]{20,512})"),
}

func placeholderSecret(value string) bool {
	v := strings.ToLower(value)
	for _, marker := range []string{"[redacted_secret]", "replace-me", "replace-with-long-random-token",
		"keep-this-codexpro-token-stable", "keep-this-stable-token", "your-ngrok-token", "your-token",
		"your-api-key-here", "<openai_api_key>", "process.env.", "import.meta.env.", "os.environ", "getenv("} {
		if strings.Contains(v, marker) {
			return true
		}
	}
	return v == "sk-..." || strings.HasSuffix(v, "=sk-...")
}
func identifierLiteral(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' {
			return false
		}
	}
	if n := strings.IndexAny(value, "=:"); n >= 0 {
		value = value[n+1:]
	}
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"'`")
	return strings.ContainsAny(value, "./:_- \t\r\n")
}
func secretCounts(text string) (map[string]int, []string) {
	counts := map[string]int{}
	ordered := []string{}
	for i, p := range secretPatterns {
		for _, m := range p.FindAllString(text, -1) {
			if placeholderSecret(m) || (i >= 7 && identifierLiteral(m)) {
				continue
			}
			if counts[m] == 0 {
				ordered = append(ordered, m)
			}
			counts[m]++
		}
	}
	return counts, ordered
}
func HasSecretValue(text string) bool { c, _ := secretCounts(text); return len(c) > 0 }
func IntroducesSecretValue(before, after string) bool {
	b, _ := secretCounts(before)
	a, _ := secretCounts(after)
	for s, n := range a {
		if n > b[s] {
			return true
		}
	}
	return false
}
func DescribeSecretMatches(text string, limit int) []string {
	if limit <= 0 {
		limit = 3
	}
	_, matches := secretCounts(text)
	out := []string{}
	seen := map[string]bool{}
	for _, v := range matches {
		description := "a token-like value"
		if n := strings.IndexAny(v, "=:"); n > 0 {
			description = strings.TrimSpace(v[:n+1]) + " \"…\""
		} else if strings.HasPrefix(v, "sk-") {
			description = "an sk-… API key"
		} else if strings.HasPrefix(v, "gh") || strings.HasPrefix(v, "github_pat_") || strings.HasPrefix(v, "npm_") {
			description = "a provider token (sk-ant-/gh*_/github_pat_/npm_)"
		} else if strings.Contains(strings.ToLower(v), "bearer") {
			description = "an Authorization: Bearer token"
		}
		r := []rune(description)
		if len(r) > 80 {
			description = string(r[:80])
		}
		if !seen[description] {
			seen[description] = true
			out = append(out, description)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}
func SecretContentBlockedError(operation, text string) error {
	matches := DescribeSecretMatches(text, 3)
	detail := strings.Join(matches, "; ")
	if detail == "" {
		detail = "a token-like value"
	}
	return &Error{Code: "secret_content_blocked", Message: "Refusing to " + operation + ": the content contains what looks like a credential (" + detail + "). " +
		"This is a content check, not a permission problem: the workspace stays writable. " +
		"If it is a real secret, keep it in an untracked env file and reference it. If it is not (an action name, id, or test fixture), " +
		"rename the identifier so it does not end in TOKEN/SECRET/KEY/PASSWORD, or use a placeholder such as [REDACTED_SECRET].",
		Data: map[string]any{"retry_unchanged": false, "secret_matches": matches}}
}
func Redact(text string) string {
	for _, index := range []int{5, 6, 3, 7, 8, 2, 4, 0, 1} {
		p := secretPatterns[index]
		text = p.ReplaceAllStringFunc(text, func(m string) string {
			if index != 5 && index != 6 && index != 2 && index != 4 && placeholderSecret(m) {
				return m
			}
			if index == 7 {
				n := strings.IndexByte(m, '=')
				if n < 0 {
					return redactedSecret
				}
				return strings.TrimRightFunc(m[:n], unicode.IsSpace) + "= " + redactedSecret
			}
			if index == 0 || index == 1 {
				return redactedSecret
			}
			sub := p.FindStringSubmatch(m)
			if len(sub) > 1 {
				return sub[1] + redactedSecret
			}
			return redactedSecret
		})
	}
	return text
}
func RedactValue(value any) any { return redactValue(value, 0) }
func redactValue(value any, depth int) any {
	if depth > 8 {
		return value
	}
	switch v := value.(type) {
	case string:
		return Redact(v)
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = redactValue(x, depth+1)
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i, x := range v {
			out[i] = Redact(x)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = redactValue(x, depth+1)
		}
		return out
	default:
		return value
	}
}
