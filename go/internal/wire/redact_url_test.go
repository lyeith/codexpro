package wire

import (
	"errors"
	"fmt"
	"net/url"
	"testing"
)

func TestRedactCredentialBearingHTTPDiagnostics(t *testing.T) {
	const origin = "https://connector.example.test/mcp"
	for _, tc := range []struct {
		name, input, want string
	}{
		{
			name: "forwarded unauthorized diagnostic",
			input: fmt.Sprintf("upstream: %v", &url.Error{
				Op:  "Post",
				URL: "https://fake-user:fake-password@connector.example.test/mcp?codexpro_token=abc&view=summary",
				Err: errors.New("HTTP 401 Unauthorized"),
			}),
			want: "upstream: Post \"" + origin + "?codexpro_token=[REDACTED_SECRET]&view=summary\": HTTP 401 Unauthorized",
		},
		{
			name:  "encoded credential key and duplicates",
			input: origin + "?%74oken=abc&TOKEN=def&credential=ghi&credential=abc%26def&status=401",
			want:  origin + "?%74oken=[REDACTED_SECRET]&TOKEN=[REDACTED_SECRET]&credential=[REDACTED_SECRET]&credential=[REDACTED_SECRET]&status=401",
		},
		{
			name:  "userinfo including username only",
			input: "HTTPS://fake-user@connector.example.test/mcp and https://fake%40name:already%20invalid%zz@connector.example.test/mcp",
			want:  "HTTPS://connector.example.test/mcp and " + origin,
		},
		{
			name:  "credential in fragment",
			input: origin + "?view=summary#view=done&access_token=abc",
			want:  origin + "?view=summary#view=done&access_token=[REDACTED_SECRET]",
		},
		{
			name:  "question mark within a raw credential",
			input: origin + "?token=abc?def&view=summary",
			want:  origin + "?token=[REDACTED_SECRET]&view=summary",
		},
		{
			name:  "ordinary URLs retain their selectors",
			input: origin + "?page_token=next&view=summary#section",
			want:  origin + "?page_token=next&view=summary#section",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Redact(tc.input); got != tc.want {
				t.Fatalf("redacted diagnostic = %q; want %q", got, tc.want)
			}
			if got := Redact(tc.want); got != tc.want {
				t.Fatalf("second redaction = %q; want %q", got, tc.want)
			}
		})
	}
}
