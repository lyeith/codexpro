package legacyworkspace

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSourceLeasePredicateMatchesCapturedTypeScript(t *testing.T) {
	raw, err := os.ReadFile("testdata/source-lease-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		SourceSHA string `json:"source_sha256"`
		Cases     []struct {
			Name     string `json:"name"`
			Raw      string `json:"raw_base64"`
			Filename string `json:"filename"`
			Valid    bool   `json:"valid"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	if oracle.SourceSHA != "ac01aa3a57e4e6fdbeb19102b62d4052f8c057c526818bea0db918b32003c515" || len(oracle.Cases) != 73 {
		t.Fatal("unexpected source predicate oracle")
	}
	for _, test := range oracle.Cases {
		t.Run(test.Name, func(t *testing.T) {
			body, err := base64.StdEncoding.DecodeString(test.Raw)
			if err != nil {
				t.Fatal(err)
			}
			_, err = SourceLease(body, test.Filename)
			if (err == nil) != test.Valid {
				t.Fatalf("Go source validator differs from exact TypeScript predicate: valid=%v error=%v", test.Valid, err)
			}
		})
	}
}

func TestIdentityWhitespaceMatchesSourceTrim(t *testing.T) {
	id := strings.Repeat("a", 48)
	for _, value := range []string{id, id + "\n", "\ufeff" + id + "\ufeff", "\u2005" + id + "\r\n"} {
		if err := sourceIdentity([]byte(value)); err != nil {
			t.Fatalf("source-valid identity rejected: %v", err)
		}
	}
	for _, value := range []string{"", strings.Repeat("A", 48), "\u0085" + id, id + "\u0085", strings.Repeat("a", 47), strings.Repeat("g", 48)} {
		if err := sourceIdentity([]byte(value)); err == nil {
			t.Fatal("source-invalid identity accepted")
		}
	}
}
