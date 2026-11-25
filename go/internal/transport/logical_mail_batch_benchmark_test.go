//go:build unix

package transport

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// BenchmarkLogicalMailBatch measures batch construction from a strict-validated
// offer. Fixed body construction, wire encoding, validation and expected-output
// checks are outside the timed loop; only logicalMailBatch runs inside it.
func BenchmarkLogicalMailBatch(b *testing.B) {
	prefixBodyBytes := logicalMailMaxTextBytes/2 + 1
	for _, tc := range []struct {
		name          string
		bodies        []string
		wantOriginals int
	}{
		{"small-ascii", []string{"complete small ASCII body"}, 1},
		{"max-ascii-64k", []string{strings.Repeat("x", logicalMailMaxBodyBytes)}, 0},
		{"max-nul-64k", []string{strings.Repeat("\x00", logicalMailMaxBodyBytes)}, 0},
		{"combined-prefix-overflow", []string{strings.Repeat("a", prefixBodyBytes), strings.Repeat("b", prefixBodyBytes)}, 1},
		{"small-first-oversized-later", []string{"small first", strings.Repeat("x", logicalMailMaxTextBytes), "small third"}, 1},
		{"escaped-small-decoded-body-overflow", []string{strings.Repeat("\x00", 3000)}, 0},
	} {
		b.Run(tc.name, func(b *testing.B) {
			encoded, err := json.Marshal(logicalMailHelperOffer("SP6", tc.bodies...))
			if err != nil {
				b.Fatal(err)
			}
			offer := logicalMailBatchFixture(b, encoded, len(tc.bodies))
			wantText, wantOriginals := logicalMailBatch(offer)
			if !strings.HasPrefix(wantText, logicalMailOfferPrefix) ||
				len(wantText) > logicalMailMaxTextBytes || len(wantOriginals) != tc.wantOriginals {
				b.Fatal("batch fixture did not produce its expected bounded selection")
			}
			var text string
			var originals []logicalMailOriginal
			b.SetBytes(int64(len(encoded)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				text, originals = logicalMailBatch(offer)
			}
			b.StopTimer()
			if text != wantText || !reflect.DeepEqual(originals, wantOriginals) {
				b.Fatal("timed batch changed its original text or delivery plan")
			}
		})
	}
}
