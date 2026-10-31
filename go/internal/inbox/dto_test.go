package inbox

import "testing"

func TestQuestionNumbersAndArrayElementsDoNotCoerce(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":"1","id":"q","project_id":"p","source":"source","title":"title","question":"question","blocking_scope":"none"}`,
		`{"schema_version":1,"id":"q","project_id":"p","source":"source","title":"title","question":"question","blocking_scope":"none","blocked_work":[null]}`,
	} {
		if _, err := parseQuestion([]byte(body)); err == nil {
			t.Fatalf("accepted a coerced DTO: %s", body)
		}
	}
	if _, err := parseQuestion([]byte(`{"schema_version":1e0,"id":"q","project_id":"p","source":"source","title":"title","question":"question","blocking_scope":"none","blocked_work":[""]}`)); err != nil {
		t.Fatal("valid number/empty blocked-work item rejected", err)
	}
}
