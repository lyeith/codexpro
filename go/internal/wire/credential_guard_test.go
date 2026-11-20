package wire

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func guardFixtureLiteral() string {
	return strings.Join([]string{"aB3d", "E5fG", "h7Jk", "L9mN", "p2Qr", "S4tU", "v6Wx", "Y8z0"}, "")
}

// Assemble synthetic guard inputs at runtime: this file contains no credential.
func guardAssignment(name, value string) string { return name + " = " + value }
func guardProperty(name, value string) string   { return strconv.Quote(name) + ": " + value }

func TestCredentialNamedIdentifiersDoNotTriggerWriteGuard(t *testing.T) {
	cases := map[string]string{
		"kotlin-runtime-factory": strings.Join([]string{
			"private val ", "sessionToken", ": RuntimeIdentifierFactory = { runtimeId() }",
		}, ""),
		"kotlin-function-type": strings.Join([]string{
			"private val ", "sessionTokenFactory", ": () -> String = { UUID.randomUUID().toString() }",
		}, ""),
		"kotlin-numbered-type": strings.Join([]string{
			"private val ", "sessionToken", ": RuntimeUUIDFactoryV2 = { runtimeId() }",
		}, ""),
		"kotlin-numbered-declaration": strings.Join([]string{
			"private val ", "sessionToken", ": RuntimeUUIDFactoryV2",
		}, ""),
		"numbered-runtime-call":     guardAssignment("sessionToken", "RuntimeUUIDFactoryV2()"),
		"numbered-runtime-member":   guardAssignment("sessionToken", "RuntimeUUIDFactoryV2.create()"),
		"numbered-runtime-property": guardAssignment("sessionToken", "RuntimeUUIDFactoryV2.current"),
		"runtime-reference":         "const " + guardAssignment("sessionToken", "RuntimeUUIDFactoryV2"),
		"ordinary-token-text":       guardAssignment("statusToken", strconv.Quote("Loading token settings for the next page")),
		"ordinary-secret-text":      guardProperty("secretDescription", strconv.Quote("The result contains no confidential values")),
		"ordinary-password-text":    guardAssignment("passwordMessage", strconv.Quote("Please enter your password to continue")),
		"ordinary-credential":       guardProperty("credentialLabel", strconv.Quote("Account settings and sign-in options")),
		"bare-runtime-name":         guardAssignment("sessionSecret", "RuntimeIdentifierFactory"),
		"low-entropy-literal":       guardAssignment("APP_SECRET", strconv.Quote(strings.Repeat("ab", 32))),
		"safe-placeholder":          guardAssignment("APP_TOKEN", strconv.Quote("[REDACTED_SECRET]")),
		"environment-reference":     guardAssignment("APP_SECRET", "process.env.APP_SECRET"),
		"private-key-label":         guardAssignment("PRIVATE_KEY_LABEL", strconv.Quote("Choose a private key file")),
		"connection-no-password":    guardAssignment("DB_SECRET", strconv.Quote("postgresql://example.invalid/demo")),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if HasSecretValue(input) || IntroducesSecretValue("", input) {
				t.Fatal("ordinary identifier or text was classified as a credential")
			}
			if names := DescribeSecretMatches(input, 3); len(names) != 0 {
				t.Fatal("ordinary input produced a credential rule name")
			}
		})
	}
}

func TestCredentialWriteGuardPreservesSpecificRules(t *testing.T) {
	suffix := strings.Repeat("A", 48)
	cases := map[string]struct {
		input string
		rule  string
	}{
		"known-api-key":   {"sk-" + suffix, "known_key"},
		"named-key":       {"pa1_" + suffix[:43], "known_key"},
		"anthropic":       {"sk-ant-" + suffix, "known_provider_token"},
		"github-short":    {"ghp_" + suffix, "known_provider_token"},
		"github-pat":      {"github_pat_" + suffix, "known_provider_token"},
		"npm":             {"npm_" + suffix, "known_provider_token"},
		"bearer":          {"Authorization: Bearer " + suffix, "authorization_bearer"},
		"command-token":   {"tool --token=" + suffix, "credential_cli_argument"},
		"ngrok":           {"ngrok config add-authtoken " + suffix, "credential_cli_argument"},
		"cloudflared":     {"cloudflared service install " + suffix, "credential_cli_argument"},
		"query":           {"https://example.invalid/?token=" + suffix, "credential_query_parameter"},
		"explicit-env":    {guardAssignment("codexpro_token", suffix), "codexpro_token_assignment"},
		"explicit-object": {"{" + guardProperty("codexpro_token", strconv.Quote(suffix)) + "}", "codexpro_token_property"},
	}
	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
			if !HasSecretValue(fixture.input) || !IntroducesSecretValue("", fixture.input) {
				t.Fatal("existing specific credential rule no longer blocks")
			}
			found := false
			for _, rule := range DescribeSecretMatches(fixture.input, 9) {
				found = found || rule == fixture.rule
			}
			if !found {
				t.Fatalf("fixed rule name %q was not returned", fixture.rule)
			}
		})
	}
}

func TestCredentialNamedRuleRequiresLiteralEvidence(t *testing.T) {
	literal := guardFixtureLiteral()
	private := string(pem.EncodeToMemory(&pem.Block{
		Type: "PRIVATE KEY", Bytes: bytes.Repeat([]byte{0x30, 0x01, 0x02, 0x03}, 180),
	}))
	rawQuote := string(rune(0x60))
	cases := map[string]string{
		"entropy-quoted":    guardAssignment("SESSION_TOKEN", strconv.Quote(literal)),
		"entropy-bare":      guardAssignment("SESSION_TOKEN", literal),
		"entropy-export":    "export " + guardAssignment("SESSION_TOKEN", literal),
		"entropy-yaml":      "  " + guardProperty("SESSION_SECRET", strconv.Quote(literal)),
		"entropy-yaml-bare": "  " + guardProperty("SESSION_SECRET", literal),
		"entropy-json":      "{" + guardProperty("sessionSecret", strconv.Quote(literal)) + "}",
		"entropy-hex":       guardAssignment("APP_SECRET", strconv.Quote(strings.Repeat("0123456789abcdef", 4))),
		"legacy-positive": strings.Join([]string{
			"OPENAI", "_API_KEY", "=", "not-a-real-credential-", "1234567890",
		}, ""),
		"private-raw":     guardAssignment("DEPLOY_PRIVATE_KEY", rawQuote+private+rawQuote),
		"private-escaped": guardAssignment("DEPLOY_PRIVATE_KEY", strconv.Quote(private)),
		"private-triple":  guardAssignment("DEPLOY_PRIVATE_KEY", "\"\"\""+private+"\"\"\""),
		"bare-bearer":     guardAssignment("SESSION_SECRET", strconv.Quote("Bearer "+strings.Repeat("B", 24))),
		"password-url":    guardAssignment("DB_SECRET", strconv.Quote("postgresql://demo:test-only@example.invalid/demo")),
		"connection-kv":   guardAssignment("DB_SECRET", strconv.Quote("Server=example.invalid;Database=demo;Password=test-only")),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if !HasSecretValue(input) {
				t.Fatal("credential literal evidence was not blocked")
			}
			found := false
			for _, rule := range DescribeSecretMatches(input, 9) {
				found = found || rule == "credential_named_assignment"
			}
			if !found {
				t.Fatal("credential-named literal did not report its fixed rule")
			}
		})
	}
}

func TestCredentialLiteralEncodingsRetainEvidence(t *testing.T) {
	literal := guardFixtureLiteral()
	var unicodeEscapes strings.Builder
	for _, r := range literal {
		fmt.Fprintf(&unicodeEscapes, "\\u%04x", r)
	}
	cases := map[string]string{
		"quoted-sixteen":                strconv.Quote(literal[:16]),
		"unicode-escaped":               "\"" + unicodeEscapes.String() + "\"",
		"escaped-double-quote":          strconv.Quote(literal[:8] + "\"" + literal[8:]),
		"escaped-single-quote":          "'" + literal[:8] + "\\'" + literal[8:] + "'",
		"raw-single-trailing-backslash": "'" + literal + "\\'",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			input := guardAssignment("SESSION_SECRET", value)
			if !HasSecretValue(input) || !IntroducesSecretValue("", input) {
				t.Fatal("encoded or short credential literal lost its evidence")
			}
		})
	}
}

func TestCredentialGuardPreservesExistingEditAndDuplicateChecks(t *testing.T) {
	literal := guardFixtureLiteral()
	original := guardAssignment("SESSION_TOKEN", strconv.Quote(literal)) + "\n"
	if IntroducesSecretValue(original+"old prose\n", original+"new prose\n") {
		t.Fatal("an unrelated edit to existing guarded content was blocked")
	}
	if !IntroducesSecretValue(original, original+original) {
		t.Fatal("a second copy of the literal was not blocked")
	}
	changed := guardAssignment("SESSION_TOKEN", strconv.Quote(literal[:len(literal)-1]+"1")) + "\n"
	if !IntroducesSecretValue(original, changed) {
		t.Fatal("replacement credential literal was not blocked")
	}
}

func TestCredentialDenialContainsRuleNamesOnly(t *testing.T) {
	identifier := "PRIVATE_" + "AccountToken"
	literal := guardFixtureLiteral()
	input := guardAssignment(identifier, strconv.Quote(literal))
	want := []string{"credential_named_assignment"}
	if names := DescribeSecretMatches(input, 3); !reflect.DeepEqual(names, want) {
		t.Fatalf("unexpected fixed rule names: %v", names)
	}
	err := SecretContentBlockedError("apply_patch", input)
	encoded, marshalErr := json.Marshal(ErrorResult(err))
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if bytes.Contains(encoded, []byte(identifier)) || bytes.Contains(encoded, []byte(literal)) ||
		bytes.Contains(encoded, []byte(input)) {
		t.Fatal("denial metadata contains submitted identifier or value")
	}
	if !bytes.Contains(encoded, []byte("credential_named_assignment")) {
		t.Fatal("denial lost its safe rule name")
	}
}

func TestCredentialLiteralBoundariesDoNotConsumeRuntimeCode(t *testing.T) {
	cases := []string{
		"sessionToken" + ": () -> RuntimeIdentifierFactory",
		guardAssignment("sessionToken", "\"ordinary \\\"quoted\\\" text\"; unrelated = "+strconv.Quote(guardFixtureLiteral())),
		guardAssignment("sessionToken", "\"aaaa\\\"aaaa\";unrelated="+strconv.Quote(guardFixtureLiteral())),
		guardAssignment("sessionToken", "\"ordinary words\" + runtimeIdentifierFactory()"),
		guardAssignment("secretText", "\"unterminated ordinary words"),
	}
	for index, input := range cases {
		if HasSecretValue(input) {
			t.Fatalf("literal boundary case %d consumed unrelated runtime content", index)
		}
	}
}
