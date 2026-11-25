//go:build unix

package transport

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func logicalMailBatchFixture(tb testing.TB, encoded []byte, wantMessages int) logicalMailOffer {
	tb.Helper()
	offer, valid := decodeLogicalMailOffer(encoded, logicalMailHelperAddress, "SP6")
	if !valid || len(offer.Messages) != wantMessages {
		tb.Fatal("batch fixture is not a strict-valid complete offer")
	}
	return offer
}

func requireLogicalMailBatchFirstOriginal(t *testing.T, input logicalMailOffer, text string, originals []logicalMailOriginal, wantMore bool) {
	t.Helper()
	if !strings.HasPrefix(text, logicalMailOfferPrefix) || len(text) > logicalMailMaxTextBytes {
		t.Fatal("batch omitted its delivery explanation or exceeded its encoded text budget")
	}
	selected := logicalMailBatchFixture(t, []byte(strings.TrimPrefix(text, logicalMailOfferPrefix)), 1)
	var compact bytes.Buffer
	if err := json.Compact(&compact, input.Messages[0]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(selected.Messages[0], compact.Bytes()) {
		t.Fatal("batch changed the first complete envelope or discarded its unknown fields")
	}
	var first logicalMailOriginal
	if err := json.Unmarshal(input.Messages[0], &first); err != nil {
		t.Fatal(err)
	}
	if len(originals) != 1 || originals[0] != first {
		t.Fatalf("delivery plan must contain only the first complete original: got %d", len(originals))
	}
	if *selected.More != wantMore || *selected.NextAfter != first.Seq || *selected.BodyBytes != len(first.Body) {
		t.Fatalf("selected prefix metadata: more=%t next_after=%d body_bytes=%d",
			*selected.More, *selected.NextAfter, *selected.BodyBytes)
	}
	if !reflect.DeepEqual(selected.Mailbox, input.Mailbox) ||
		selected.LogicalRecipient != input.LogicalRecipient || *selected.MaxBodyBytes != *input.MaxBodyBytes {
		t.Fatal("batch changed mailbox routing or the backend body budget")
	}
}

func TestLogicalMailBatchCombinedBodyPrefixOverBudget(t *testing.T) {
	// Either complete original fits alone; their decoded bodies together exceed
	// the entire text budget even before routing fields and the offer prefix.
	bodyBytes := logicalMailMaxTextBytes/2 + 1
	encoded := logicalMailHelperJSON(t, logicalMailHelperOffer("SP6",
		strings.Repeat("a", bodyBytes), strings.Repeat("b", bodyBytes)))
	offer := logicalMailBatchFixture(t, encoded, 2)
	text, originals := logicalMailBatch(offer)
	requireLogicalMailBatchFirstOriginal(t, offer, text, originals, true)
}

func TestLogicalMailBatchCompactsRawMessageWhitespace(t *testing.T) {
	encoded := logicalMailHelperJSON(t, logicalMailHelperOffer("SP6", "complete small body"))
	// Put removable whitespace inside the message object, so RawMessage keeps
	// it through strict decoding. Its raw length is not a safe output lower bound.
	padded := bytes.Replace(encoded, []byte("\"messages\":[{"),
		[]byte("\"messages\":[{"+strings.Repeat(" ", logicalMailMaxTextBytes+1)), 1)
	if bytes.Equal(padded, encoded) {
		t.Fatal("fixture did not insert message whitespace")
	}
	offer := logicalMailBatchFixture(t, padded, 1)
	if len(offer.Messages[0]) <= logicalMailMaxTextBytes {
		t.Fatal("fixture did not retain an oversized raw message")
	}
	text, originals := logicalMailBatch(offer)
	requireLogicalMailBatchFirstOriginal(t, offer, text, originals, false)
}
