package process

import "testing"

func TestRedactCredentialBearingHTTPToolOutput(t *testing.T) {
	input := `connector returned HTTP 401: "https://fake-user:fake-password@connector.example.test/mcp?%74oken=abc&view=summary"`
	want := `connector returned HTTP 401: "https://connector.example.test/mcp?%74oken=[REDACTED_SECRET]&view=summary"`
	if got := RedactSensitiveText(input); got != want {
		t.Fatalf("rendered tool output = %q; want %q", got, want)
	}
}
