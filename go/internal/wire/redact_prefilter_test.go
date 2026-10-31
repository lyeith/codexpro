package wire

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode"
)

func redactPrefilterCorpus() []string {
	corpus := []string{
		"", "ordinary path: src/work/history.go", "ordinary identifier = value",
		"προβολή 漢字 😀 plain content", "\xff\xfe non-UTF8",
		strings.Repeat("ordinary-word/path.without-credential-markers ", 1024),
		"token counts: key names and secret placeholders are documentation",
		"APIKEY: short", "TOKEN=short", "PRIVATE_KEY = [REDACTED_SECRET]",
	}
	long := strings.Repeat("A1b_9", 110)
	for _, length := range []int{0, 7, 8, 9, 11, 12, 19, 20, 511, 512, 513} {
		value := long[:length]
		for _, form := range []string{
			"sk-%s", "sk-ant-%s", "ghp_%s", "gho_%s", "ghu_%s", "ghs_%s", "ghr_%s",
			"github_pat_%s", "npm_%s", "Authorization: Bearer %s",
			"ngrok config add-authtoken %s", "cloudflared service install %s",
			"--token=%s", "--access-token %s", "--auth-token=%s",
			"--api_key %s", "--api-key=%s", "--apikey %s", "--authtoken=%s",
			"?codexpro_token=%s&next=1", "&token=%s", "?access_token=%s", "?auth_token=%s", "?api_key=%s",
			"codexpro_token = \"%s\"", "'codexpro_token': '%s'",
			"TOKEN = %s", "APP_SECRET_suffix = '%s'", "Password=\"%s\"",
			"api_key : \"%s\"", "private-key: '%s'", "PRIVATEKEY=" + string(rune(96)) + "%s" + string(rune(96)),
			"APP_ſECRET = '%s'", "PRIVATE_KEY: \"%s\"", "codeXpro_toKen=%s",
			"AUTHORIZATION:\tBEARER %s",
		} {
			corpus = append(corpus, fmt.Sprintf(form, value))
		}
	}
	value := long[:40]
	for _, prefix := range []string{"", " ", "word", strings.Repeat("P", 64), strings.Repeat("P", 65), "é", "ſ", "K"} {
		for _, suffix := range []string{"", "\n", "\r\n", "\"", "&next=x", "tail"} {
			corpus = append(corpus, prefix+"APP_TOKEN="+value+suffix)
		}
	}
	for _, placeholder := range []string{
		"[REDACTED_SECRET]", "replace-me", "replace-with-long-random-token",
		"keep-this-codexpro-token-stable", "keep-this-stable-token", "your-ngrok-token",
		"your-token", "your-api-key-here", "<openai_api_key>", "process.env.VALUE",
		"import.meta.env.VALUE", "os.environ.VALUE", "getenv(VALUE)", "sk-...",
	} {
		corpus = append(corpus, "TOKEN='"+placeholder+"'", "?codexpro_token="+placeholder, "--token="+placeholder)
	}
	corpus = append(corpus,
		"APP_TOKEN=\""+value+"\"\nAuthorization: Bearer "+value+" ?token="+value,
		"--token="+value+" SECRET='"+value+"' codexpro_token="+value,
		"APP_ſECRET='"+value+"' PRIVATE_KEY='"+value+"' --toKen="+value,
		"\xffAuthorization: Bearer "+value+"\xfe",
		"TOKEN="+strings.Repeat(" ", 4096)+value,
		"TOKEN:"+strings.Repeat("\t", 4096)+"'"+value+"'",
		"API_ＫEY='"+value+"' SEＣRET='"+value+"' prİvate_key='"+value+"'",
	)
	random := rand.New(rand.NewSource(6))
	alphabet := []byte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:-='\"?& \t\n")
	for index := 0; index < 128; index++ {
		noise := make([]byte, random.Intn(512))
		for n := range noise {
			noise[n] = alphabet[random.Intn(len(alphabet))]
		}
		form := corpus[random.Intn(len(corpus))]
		cut := random.Intn(len(noise) + 1)
		corpus = append(corpus, string(noise[:cut])+form+string(noise[cut:]))
	}
	return corpus
}

// The original replacement function below is copied verbatim from the
// pre-optimization source SHA 954899fedef3f059d0aac7ec52c8163d1a3c834d8abdb1c8be79d5dd2a91ef37.
// Patterns, order, placeholder rules and callbacks remain identical. This
// oracle never calls the new prefilter.
func TestRedactRequiredLiteralPrefilterEquivalence(t *testing.T) {
	for index, value := range redactPrefilterCorpus() {
		expected, actual := originalRegexRedact(value), Redact(value)
		if actual != expected {
			t.Fatalf("redaction output differs from unfiltered oracle at synthetic case %d", index)
		}
		filter := secretPatternPrefilter{text: value}
		for pattern, expression := range secretPatterns {
			if expression.MatchString(value) && !filter.possible(pattern) {
				t.Fatalf("prefilter rejected a true original match at synthetic case %d pattern %d", index, pattern)
			}
		}
	}
}

func TestSecretPrefilterUnicodeSimpleFoldAliases(t *testing.T) {
	if secretASCIIAliases['ſ'] != 's' || secretASCIIAliases['K'] != 'k' {
		t.Fatal("required Unicode simple-fold aliases are absent")
	}
	value := strings.Repeat("A1b_9", 8)
	for _, input := range []string{"APP_ſECRET='" + value + "'", "PRIVATE_KEY: '" + value + "'", "codexpro_toKen=" + value} {
		expected := originalRegexRedact(input)
		if expected == input {
			t.Fatal("Unicode alias fixture does not exercise a matching original expression")
		}
		if Redact(input) != expected {
			t.Fatal("Unicode simple-fold credential was weakened by the prefilter")
		}
	}
}

func FuzzRedactRequiredLiteralPrefilterEquivalence(f *testing.F) {
	corpus := redactPrefilterCorpus()
	for _, index := range []int{0, 1, 2, 3, 4, 6, 8, 49, 147, 219} {
		f.Add(corpus[index])
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 8192 {
			t.Skip()
		}
		if Redact(value) != originalRegexRedact(value) {
			t.Fatal("prefilter changed the original redaction bytes")
		}
	})
}

func originalRegexRedact(text string) string {
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
