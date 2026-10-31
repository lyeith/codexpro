package usage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestNumericDTOsRejectJSONStringsAndNull(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	s, err := Open(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, value := range []string{`"1"`, `null`, `true`} {
		_, err = s.RequestReset(ctx, []byte(fmt.Sprintf(`{"schema_version":1,"request_id":"bad","expected_revision":%s}`, value)), "operator")
		requireStatus(t, err, 400)
	}
	for _, percent := range []string{`null`, `"0"`, `false`} {
		_, err = s.SyncLimits(ctx, []byte(fmt.Sprintf(`{"schema_version":1,"checked_at":"2026-10-31T12:00:00Z","weekly":{"resets_at":"2026-11-06T12:00:00Z","window_minutes":10080,"used_percent":%s}}`, percent)))
		requireStatus(t, err, 400)
	}
	_, err = s.SyncLimits(ctx, []byte(`{"schema_version":1,"checked_at":"2026-10-31T12:00:00Z","reset_request_id":null,"weekly":null}`))
	requireStatus(t, err, 400)
	_, err = s.Ingest(ctx, []byte(`{"schema_version":1,"collector_id":"c","submissions":[],"coverage":{"unconfirmed":"0","failed_before_send":0,"unknown_mode":0,"oldest_record_at":null}}`))
	requireStatus(t, err, 400)
	// A JSON number written with an exponent remains a number, as in Zod.
	if _, err = s.RequestReset(ctx, []byte(`{"schema_version":1e0,"request_id":"valid","expected_revision":1.0}`), "operator"); err != nil {
		t.Fatal(err)
	}
}
