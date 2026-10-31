package inbox

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func expectStatus(t *testing.T, err error, status int) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Status != status {
		t.Fatalf("wanted %d got %v", status, err)
	}
}
func question(id string) []byte {
	return []byte(fmt.Sprintf("{\"schema_version\":1,\"id\":%q,\"project_id\":\"p\",\"source\":\" worker \",\"title\":\" title \",\"question\":\" question \",\"blocking_scope\":\"ticket\"}", id))
}
func TestPublishAnswerDeliveryAndReplay(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	dir := filepath.Join(t.TempDir(), "inbox")
	clock := func() time.Time { return now }
	s, e := Open(dir, clock)
	if e != nil {
		t.Fatal(e)
	}
	item, e := s.Publish(ctx, question("q"))
	if e != nil || item.Source != "worker" || item.Revision != 1 || len(item.Options) != 0 {
		t.Fatalf("%#v %v", item, e)
	}
	replay, e := s.Publish(ctx, question("q"))
	if e != nil || replay.CreatedAt != item.CreatedAt {
		t.Fatalf("%#v %v", replay, e)
	}
	answer := []byte("{\"schema_version\":1,\"request_id\":\"a1\",\"expected_revision\":1,\"answer\":\" yes \"}")
	first, e := s.Answer(ctx, "p", "q", answer, "operator")
	if e != nil || first.Answer.Text != "yes" || first.Revision != 2 {
		t.Fatalf("%#v %v", first, e)
	}
	now = now.Add(time.Second)
	delivered, e := s.Delivered(ctx, "p", "q", []byte("{\"schema_version\":1,\"consumer\":\"worker\",\"revision\":2}"))
	if e != nil || delivered.Revision != 2 || delivered.UpdatedAt != first.UpdatedAt || len(delivered.Deliveries) != 1 {
		t.Fatalf("%#v %v", delivered, e)
	}
	consumer := "worker"
	list, e := s.List(ctx, ListFilter{Consumer: &consumer})
	if e != nil || list.Total != 0 {
		t.Fatalf("%#v %v", list, e)
	}
	now = now.Add(time.Second)
	second, e := s.Answer(ctx, "p", "q", []byte("{\"schema_version\":1,\"request_id\":\"a2\",\"expected_revision\":2,\"answer\":\"no\"}"), "operator")
	if e != nil || second.Revision != 3 {
		t.Fatalf("%#v %v", second, e)
	}
	list, e = s.List(ctx, ListFilter{Consumer: &consumer})
	if e != nil || list.Total != 1 {
		t.Fatalf("%#v %v", list, e)
	}
	_, e = s.Delivered(ctx, "p", "q", []byte("{\"schema_version\":1,\"consumer\":\"worker\",\"revision\":2}"))
	expectStatus(t, e, 409)
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(dir, clock)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	old, e := s.Answer(ctx, "p", "q", answer, "another")
	if e != nil || old.Revision != 2 || old.Answer.By != "operator" {
		t.Fatalf("replay changed: %#v %v", old, e)
	}
	events, e := s.Events(ctx, nil, 0, 100)
	if e != nil || len(events) != 4 {
		t.Fatalf("%#v %v", events, e)
	}
}
func TestConcurrentAnswerRevisionHasSingleWinner(t *testing.T) {
	ctx := context.Background()
	s, e := Open(t.TempDir(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Publish(ctx, question("q")); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := s.Answer(ctx, "p", "q", []byte(fmt.Sprintf("{\"schema_version\":1,\"request_id\":\"a%d\",\"expected_revision\":1,\"answer\":\"yes\"}", i)), "operator")
			results <- e
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else {
			expectStatus(t, e, 409)
		}
	}
	if success != 1 {
		t.Fatalf("winners=%d", success)
	}
}
func TestStrictQuestionAndConsumerRevision(t *testing.T) {
	ctx := context.Background()
	s, e := Open(t.TempDir(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for _, data := range []string{
		"{\"schema_version\":1,\"id\":\"q\",\"project_id\":\"p\",\"source\":\"s\",\"title\":\"t\",\"question\":\"q\",\"blocking_scope\":\"ticket\",\"options\":null}",
		"{\"schema_version\":1,\"id\":\"q\",\"project_id\":\"p\",\"source\":\"s\",\"title\":\"t\",\"question\":\"q\",\"blocking_scope\":\"ticket\",\"source_url\":\"javascript:alert(1)\"}",
	} {
		_, e = s.Publish(ctx, []byte(data))
		expectStatus(t, e, 400)
	}
	if _, e = s.Publish(ctx, question("q")); e != nil {
		t.Fatal(e)
	}
	_, e = s.Delivered(ctx, "p", "q", []byte("{\"schema_version\":1,\"consumer\":\"c\",\"revision\":1}"))
	expectStatus(t, e, 409)
}
