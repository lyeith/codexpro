// Package status renders the owner-only operations snapshot without live probes.
package status

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed page.html
var pageHTML string

type Stamp struct {
	Known bool      `json:"known"`
	At    time.Time `json:"at"`
}
type Decision struct {
	ID             string `json:"id"`
	Question       string `json:"question"`
	Recommendation string `json:"recommendation"`
}
type Row struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}
type Work struct {
	Stamp
	Ready    *int  `json:"ready"`
	Pilot    *int  `json:"pilot"`
	Progress *int  `json:"progress"`
	Held     *int  `json:"held"`
	Landed   *int  `json:"landed"`
	Oldest   []Row `json:"oldest"`
}
type Person struct {
	Name  string     `json:"name"`
	Role  string     `json:"role"`
	State string     `json:"state"`
	Last  *time.Time `json:"last,omitempty"`
	Row   *Row       `json:"row,omitempty"`
}
type People struct {
	Mail      Stamp    `json:"mail"`
	Activity  Stamp    `json:"activity"`
	Rows      []Person `json:"rows"`
	Pending   *int     `json:"pending,omitempty"`
	Mailboxes *int     `json:"mailboxes,omitempty"`
}
type Batch struct {
	Group       string    `json:"group"`
	State       string    `json:"state"`
	Refs        []string  `json:"refs"`
	Before      string    `json:"before"`
	After       string    `json:"after,omitempty"`
	Seconds     *int      `json:"seconds"`
	Started     time.Time `json:"started"`
	Publication string    `json:"publication,omitempty"`
}
type Landing struct {
	Stamp
	Current    *Batch   `json:"current,omitempty"`
	Recent     []Batch  `json:"recent"`
	Queue      []string `json:"queue"`
	QueueKnown bool     `json:"queue_known"`
}
type Phase struct {
	Name    string   `json:"name"`
	State   string   `json:"state"`
	Seconds *float64 `json:"seconds,omitempty"`
}
type Nightly struct {
	CUJSeconds *float64 `json:"cuj_seconds,omitempty"`
	Stamp
	Run     string    `json:"run"`
	State   string    `json:"state"`
	Started time.Time `json:"started"`
	Phases  []Phase   `json:"phases"`
}
type SystemFact struct {
	Stamp
	Name  string `json:"name"`
	Value string `json:"value"`
}
type Snapshot struct {
	Version   int       `json:"version"`
	At        time.Time `json:"at"`
	Decisions struct {
		Stamp
		Items []Decision `json:"items"`
	} `json:"decisions"`
	Work    Work         `json:"work"`
	People  People       `json:"people"`
	Landing Landing      `json:"landing"`
	Nightly Nightly      `json:"nightly"`
	System  []SystemFact `json:"system"`
}
type pageData struct {
	Snapshot
	Available                      bool
	Now                            time.Time
	Alerts                         []string
	Passed, Failed, Unrun, Unknown int
	AttentionKnown                 bool
}

func readSnapshot(path string) (Snapshot, error) {
	var data Snapshot
	dir, e := os.Lstat(filepath.Dir(path))
	if e != nil || !dir.IsDir() || dir.Mode().Perm() != 0700 {
		return data, errors.New("unavailable")
	}
	fi, e := os.Lstat(path)
	if e != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0600 || fi.Size() > 256<<10 {
		return data, errors.New("unavailable")
	}
	f, e := os.Open(path)
	if e != nil {
		return data, e
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(fi, opened) {
		return data, errors.New("unavailable")
	}
	raw, e := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if e != nil || len(raw) > 256<<10 {
		return data, errors.New("unavailable")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e = d.Decode(&data); e != nil {
		return data, e
	}
	if d.Decode(new(any)) != io.EOF || data.Version != 1 || data.At.IsZero() || len(data.Decisions.Items) > 64 || len(data.Work.Oldest) > 8 || len(data.People.Rows) > 32 || len(data.Landing.Recent) > 5 || len(data.Nightly.Phases) > 48 || len(data.System) > 16 {
		return data, errors.New("invalid snapshot")
	}
	// A missing owner field is unknown. Decoding it as an ordinary zero
	// would turn an incomplete observation into an empty queue or a success.
	if data.Work.Known {
		for _, n := range []*int{data.Work.Ready, data.Work.Pilot, data.Work.Progress, data.Work.Held, data.Work.Landed} {
			if n == nil || *n < 0 {
				data.Work.Known = false
				break
			}
		}
		if data.Work.Known && (*data.Work.Pilot > *data.Work.Ready || data.Work.Oldest == nil) {
			data.Work.Known = false
		}
	}
	if data.Decisions.Items == nil || !data.Decisions.Known {
		data.Decisions.Known = false
		data.Decisions.Items = nil
	}
	if data.People.Mail.Known && (data.People.Pending == nil || data.People.Mailboxes == nil || *data.People.Pending < 0 || *data.People.Mailboxes < 0) {
		data.People.Mail.Known = false
	}
	if data.Landing.Known && (data.Landing.Queue == nil || data.Landing.Recent == nil) {
		data.Landing.Known = false
	}
	return data, nil
}

var page = template.Must(template.New("status").Funcs(template.FuncMap{
	"age": func(t time.Time) string {
		d := max(time.Duration(0), time.Since(t)).Round(time.Second)
		return d.String() + " ago"
	},
	"old": func(t time.Time) bool { return !t.IsZero() && time.Since(t) > 3*time.Minute },
	"shortSHA": func(s string) string {
		if len(s) < 10 {
			return "unknown"
		}
		return s[:10]
	},
	"iso":     func(v time.Time) string { return v.UTC().Format(time.RFC3339) },
	"utc":     func(v time.Time) string { return v.UTC().Format("2 Jan 2006 15:04 UTC") },
	"state":   func(s string) string { return strings.ReplaceAll(s, "_", " ") },
	"join":    strings.Join,
	"stale":   func(t, now time.Time) bool { return !t.IsZero() && now.Sub(t) > 3*time.Minute },
	"elapsed": func(s int) string { return (time.Duration(s) * time.Second).String() },
	"seconds": func(n float64) string { return (time.Duration(n) * time.Second).String() },
}).Parse(pageHTML))

func Register(mux *http.ServeMux) {
	root := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(root) {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		root = filepath.Join(home, ".local", "state")
	}
	mux.Handle("/status", Handler(filepath.Join(root, "plans-status", "status.json")))
}
func Handler(path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/status" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		if r.Method == http.MethodHead {
			return
		}
		snap, err := readSnapshot(path)
		v := pageData{Now: time.Now().UTC(), Available: err == nil}
		if err == nil {
			v.Snapshot = snap
			if len(snap.Landing.Recent) > 0 && snap.Landing.Known {
				b := snap.Landing.Recent[0]
				if b.State == "failed" {
					v.Alerts = append(v.Alerts, "The latest landing failed.")
				} else if b.Publication == "failed" {
					v.Alerts = append(v.Alerts, "Main landed, but frontend publication failed.")
				}
			}
			for _, p := range snap.People.Rows {
				if p.State == "dead" {
					v.Alerts = append(v.Alerts, p.Name+" is reported dead.")
				}
			}
			for _, fact := range snap.System {
				if fact.Known && fact.Name == "Main comparison" {
					v.AttentionKnown = snap.Decisions.Known && snap.Landing.Known && snap.People.Mail.Known && v.Now.Sub(snap.At) <= 3*time.Minute
				}
				if fact.Known && fact.Name == "Main comparison" && fact.Value == "different" {
					v.Alerts = append(v.Alerts, "SSD and GitHub main differ.")
				}
			}
			for _, p := range snap.Nightly.Phases {
				switch p.State {
				case "passed":
					v.Passed++
				case "failed":
					v.Failed++
				case "not_run":
					v.Unrun++
				default:
					v.Unknown++
				}
			}
		}
		var b bytes.Buffer
		if err := page.Execute(&b, v); err != nil {
			http.Error(w, "Status unavailable", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(b.Bytes())
	})
}
