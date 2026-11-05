package status

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "status.json")
}
func statusFixture(at time.Time) Snapshot {
	st := Stamp{Known: true, At: at}
	s := Snapshot{Version: 1, At: at}
	s.Decisions.Stamp = st
	s.Decisions.Items = []Decision{{ID: "D-12", Question: "Approve the pilot date?", Recommendation: "Use Friday's rehearsal."}}
	ready, pilot, progress, held, landed := 9, 2, 4, 3, 1
	s.Work = Work{Stamp: st, Ready: &ready, Pilot: &pilot, Progress: &progress, Held: &held, Landed: &landed, Oldest: []Row{{19, "Supplier onboarding"}, {24, "US gas factors"}}}
	pending, boxes := 3, 4
	s.People = People{Mail: st, Activity: st, Pending: &pending, Mailboxes: &boxes, Rows: []Person{
		{Name: "Coordinator", Role: "Coordinator", State: "live", Last: &at},
		{Name: "Concierge", Role: "Concierge", State: "idle", Last: &at},
		{Name: "SP-2", Role: "Engineering", State: "live", Last: &at, Row: &Row{315, "Result questions in Requests"}},
	}}
	seconds := 62
	tsec := 44.2
	s.Landing = Landing{Stamp: st, QueueKnown: true, Queue: []string{"land/footprint-result"}, Recent: []Batch{
		{Group: "batch-20261105-130000", State: "passed", Refs: []string{"land/checked-restart"}, Before: strings.Repeat("1", 40), After: strings.Repeat("2", 40), Seconds: &seconds, Started: at, Publication: "failed"},
	}}
	s.Nightly = Nightly{Stamp: st, Run: "20261105T010000Z", State: "failed", Started: at, Phases: []Phase{
		{Name: "land-check", State: "passed", Seconds: &tsec}, {Name: "acceptance", State: "passed", Seconds: &tsec}, {Name: "features", State: "failed", Seconds: &tsec}, {Name: "cuj", State: "not_run"},
	}}
	s.System = []SystemFact{{Stamp: st, Name: "SSD stacks", Value: "1 running · 1 stopped"}, {Stamp: st, Name: "Main comparison", Value: "in sync"}, {Stamp: Stamp{At: at}, Name: "GitHub main"}}
	return s
}
func saveFixture(t *testing.T, path string, s Snapshot) {
	t.Helper()
	raw, err := json.Marshal(s)
	if err == nil {
		err = os.WriteFile(path, raw, 0600)
	}
	if err != nil {
		t.Fatal(err)
	}
}
func renderFixture(t *testing.T, path, method string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRecorder()
	Handler(path).ServeHTTP(r, httptest.NewRequest(method, "/status", nil))
	return r
}
func TestStatusPageExactFactsUnknownAndFreshness(t *testing.T) {
	path := fixtureFile(t)
	s := statusFixture(time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC))
	saveFixture(t, path, s)
	r := renderFixture(t, path, http.MethodGet)
	body := r.Body.String()
	if r.Code != 200 {
		t.Fatalf("status %d %s", r.Code, body)
	}
	for _, want := range []string{"Approve the pilot date?", "Main landed, but frontend publication failed.", "SP-2", "Result questions in Requests", "land/footprint-result", "2222222222", "Supplier onboarding", "2 passed · 1 failed · 1 not run", "CUJ budget 60 min", "Couldn't read GitHub main", "Snapshot over 3 minutes old"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	names := []string{"Needs you", "Who's working", "Landing", "Work", "Nightly", "System"}
	index := -1
	for _, name := range names {
		next := strings.Index(body, "<h2>"+name+"</h2>")
		if next <= index {
			t.Fatalf("section order at %s", name)
		}
		index = next
	}
	if h := renderFixture(t, path, http.MethodHead); h.Code != 200 || h.Body.Len() != 0 || h.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("HEAD response differs")
	}
	s.Decisions.Items = []Decision{}
	s.Landing.Recent = []Batch{}
	s.At = time.Now().UTC()
	s.Decisions.At = s.At
	s.Landing.At = s.At
	s.People.Mail.At = s.At
	saveFixture(t, path, s)
	if !strings.Contains(renderFixture(t, path, "GET").Body.String(), "Nothing needs you") {
		t.Fatal("missing known empty decision state")
	}
	s.Decisions.Known = false
	saveFixture(t, path, s)
	b := renderFixture(t, path, "GET").Body.String()
	if !strings.Contains(b, "Couldn't read decisions") || strings.Contains(b, "Nothing needs you") {
		t.Fatal("lost unknown distinction")
	}
}
func TestMissingWorkCountDoesNotBecomeZero(t *testing.T) {
	path := fixtureFile(t)
	s := statusFixture(time.Now().UTC())
	s.Work.Held = nil
	saveFixture(t, path, s)
	b := renderFixture(t, path, "GET").Body.String()
	if !strings.Contains(b, "Couldn't read the work registry") || strings.Contains(b, "Ready in total") {
		t.Fatal("incomplete work counts were presented as known")
	}
}

func TestStatusRejectsUnreadableOrUnsafeSnapshot(t *testing.T) {
	for _, mode := range []string{"absent", "oversize", "public", "symlink", "extra field", "invalid JSON"} {
		t.Run(mode, func(t *testing.T) {
			p := fixtureFile(t)
			switch mode {
			case "public":
				saveFixture(t, p, statusFixture(time.Now()))
				if err := os.Chmod(p, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				saveFixture(t, p+".other", statusFixture(time.Now()))
				if err := os.Symlink(p+".other", p); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(p, []byte(strings.Repeat("x", 256*1024+1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "extra field":
				if err := os.WriteFile(p, []byte(`{"version":1,"extra":"PRIVATE-FIXTURE"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid JSON":
				if err := os.WriteFile(p, []byte("{PRIVATE-FIXTURE"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			body := renderFixture(t, p, "GET").Body.String()
			if !strings.Contains(body, "Couldn't read the status snapshot") || strings.Contains(body, "PRIVATE-FIXTURE") || strings.Contains(body, "Nothing needs you") {
				t.Fatal("unsafe unavailable view")
			}
		})
	}
}
