//go:build unix

package transport

import (
	"encoding/json"
	"strings"
	"testing"
)

// BenchmarkDecodeLogicalMailOffer measures only validation and decoding. Every
// case uses the same valid single-message envelope as the logical-mail fixtures;
// encoding, fixture validation and body construction are outside the timed loop.
func BenchmarkDecodeLogicalMailOffer(b *testing.B) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"small-ascii", "complete small ASCII body"},
		{"max-ascii-64k", strings.Repeat("x", logicalMailMaxBodyBytes)},
		{"max-nul-64k", strings.Repeat("\x00", logicalMailMaxBodyBytes)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			encoded, err := json.Marshal(logicalMailHelperOffer("SP6", tc.body))
			if err != nil {
				b.Fatal(err)
			}
			if offer, ok := decodeLogicalMailOffer(encoded, logicalMailHelperAddress, "SP6"); !ok || len(offer.Messages) != 1 {
				b.Fatal("benchmark fixture is not a valid single-message offer")
			}
			b.SetBytes(int64(len(encoded)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, ok := decodeLogicalMailOffer(encoded, logicalMailHelperAddress, "SP6"); !ok {
					b.Fatal("valid benchmark offer was rejected")
				}
			}
		})
	}
}
