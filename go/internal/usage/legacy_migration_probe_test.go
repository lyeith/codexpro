package usage_test

// Opt-in native-store migration proof. The Python preparer creates two
// disposable copies; this probe opens only those copies through the real Go
// public readers and replays already stored reset/answer receipts.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/inbox"
	"github.com/lyeith/codexpro/go/internal/usage"
)

type migrationStore struct {
	Directory     string `json:"directory"`
	Database      string `json:"database"`
	InitialSHA256 string `json:"initial_sha256"`
	Archive       string `json:"archive"`
}
type migrationPlan struct {
	SchemaVersion  int                       `json:"schema_version"`
	SourceCommit   string                    `json:"source_commit"`
	Disposable     bool                      `json:"disposable_copy"`
	Clock          string                    `json:"clock"`
	Expected       string                    `json:"expected"`
	ExpectedSHA256 string                    `json:"expected_sha256"`
	Stores         map[string]migrationStore `json:"stores"`
}
type migrationReset struct {
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
	Result  json.RawMessage `json:"result"`
}
type migrationAnswer struct {
	Project string          `json:"project"`
	ID      string          `json:"id"`
	Request string          `json:"request"`
	Payload json.RawMessage `json:"payload"`
	Result  json.RawMessage `json:"result"`
}
type migrationExpected struct {
	Usage struct {
		Snapshots []struct {
			Project *string `json:"project"`
		} `json:"snapshots"`
		Resets []migrationReset `json:"reset_receipts"`
	} `json:"usage"`
	Inbox struct {
		Listings []struct {
			Project *string `json:"project"`
		} `json:"listings"`
		Answers []migrationAnswer `json:"answer_receipts"`
	} `json:"inbox"`
}

func migrationWithin(root, value string) bool {
	rel, err := filepath.Rel(root, value)
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
func migrationPrivate(name string) (string, error) {
	value, err := filepath.EvalSymlinks(name)
	if err != nil {
		return "", err
	}
	value, err = filepath.Abs(value)
	if err != nil {
		return "", err
	}
	homes := []string{}
	if h, e := os.UserHomeDir(); e == nil {
		homes = append(homes, h)
	}
	if u, e := user.Current(); e == nil {
		homes = append(homes, u.HomeDir)
	}
	for _, home := range homes {
		for _, suffix := range []string{".codexpro", filepath.Join(".config", "codexpro")} {
			blocked := filepath.Join(home, suffix)
			if migrationWithin(blocked, value) {
				return "", errors.New("protected live state refused")
			}
			if real, e := filepath.EvalSymlinks(blocked); e == nil && migrationWithin(real, value) {
				return "", errors.New("protected live state alias refused")
			}
		}
	}
	return value, nil
}
func migrationHash(name string, maximum int64) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() || st.Size() > maximum {
		return "", errors.New("migration input is not a bounded regular file")
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	after, err := f.Stat()
	if err != nil {
		return "", err
	}
	if st.Size() != after.Size() || !st.ModTime().Equal(after.ModTime()) || !os.SameFile(st, after) {
		return "", errors.New("migration input changed")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func migrationReadJSON(name string, maximum int64, target any) error {
	if _, err := migrationHash(name, maximum); err != nil {
		return err
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
func migrationError(err error) any {
	if err == nil {
		return nil
	}
	value := map[string]any{"probe_error": err.Error()}
	var ue *usage.Error
	var ie *inbox.Error
	if errors.As(err, &ue) {
		value["http_status"] = ue.Status
	}
	if errors.As(err, &ie) {
		value["http_status"] = ie.Status
	}
	return value
}

func TestLegacyMigrationProbe(t *testing.T) {
	planName := os.Getenv("SP6_LEGACY_PLAN")
	if planName == "" {
		t.Skip("explicit disposable legacy telemetry fixture not supplied")
	}
	resultName := os.Getenv("SP6_LEGACY_RESULT")
	if resultName == "" {
		t.Fatal("SP6_LEGACY_RESULT is required")
	}
	planPath, err := migrationPrivate(planName)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(planPath)
	var plan migrationPlan
	if err = migrationReadJSON(planPath, 4*1024*1024, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != 1 || plan.SourceCommit != "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a" || !plan.Disposable {
		t.Fatal("invalid disposable migration plan")
	}
	clock, err := time.Parse(time.RFC3339Nano, plan.Clock)
	if err != nil || clock.UTC().Format("2006-01-02T15:04:05.000Z") != plan.Clock {
		t.Fatal("invalid fixed migration clock")
	}
	expectedPath, err := migrationPrivate(plan.Expected)
	if err != nil || !migrationWithin(root, expectedPath) {
		t.Fatal("expected values escape fixture")
	}
	hash, err := migrationHash(expectedPath, 16*1024*1024)
	if err != nil || hash != plan.ExpectedSHA256 {
		t.Fatal("expected values differ from plan")
	}
	var expected migrationExpected
	if err = migrationReadJSON(expectedPath, 16*1024*1024, &expected); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"usage", "inbox"} {
		store, exists := plan.Stores[kind]
		if !exists {
			t.Fatal("missing planned native store")
		}
		file, err := migrationPrivate(store.Database)
		if err != nil {
			t.Fatal(err)
		}
		directory, err := migrationPrivate(store.Directory)
		if err != nil {
			t.Fatal(err)
		}
		if !migrationWithin(root, file) || file != filepath.Join(directory, kind+".sqlite") {
			t.Fatal("native store escapes disposable fixture")
		}
		link, err := os.Lstat(store.Database)
		if err != nil || link.Mode()&os.ModeSymlink != 0 {
			t.Fatal("consumer database must be a real file")
		}
		archive, err := migrationPrivate(store.Archive)
		if err != nil {
			t.Fatal(err)
		}
		original, err := os.Stat(archive)
		if err != nil {
			t.Fatal(err)
		}
		if archive == file || os.SameFile(original, link) {
			t.Fatal("consumer is not a separate copy of the published archive")
		}
		for _, name := range []string{file, archive} {
			digest, err := migrationHash(name, 128*1024*1024)
			if err != nil || digest != store.InitialSHA256 {
				t.Fatal("archive or consumer digest changed before Go open")
			}
		}
		for _, suffix := range []string{"-wal", "-journal", "-shm"} {
			if _, err = os.Stat(file + suffix); !os.IsNotExist(err) {
				t.Fatal("disposable database is not normalized")
			}
		}
	}
	output, err := filepath.Abs(resultName)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := migrationPrivate(filepath.Dir(output))
	if err != nil || parent != filepath.Dir(output) || !migrationWithin(root, output) {
		t.Fatal("result escapes private fixture")
	}
	if _, err = os.Lstat(output); !os.IsNotExist(err) {
		t.Fatal("result file already exists")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	u, err := usage.Open(plan.Stores["usage"].Directory, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	i, err := inbox.Open(plan.Stores["inbox"].Directory, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	snapshots := []map[string]any{}
	for _, request := range expected.Usage.Snapshots {
		value, err := u.Snapshot(ctx, request.Project)
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, map[string]any{"project": request.Project, "value": value})
	}
	history, err := u.History(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resets := []map[string]any{}
	for _, request := range expected.Usage.Resets {
		value, err := u.RequestReset(ctx, request.Payload, "migration_probe")
		var result any = value
		if err != nil {
			result = migrationError(err)
		}
		resets = append(resets, map[string]any{"id": request.ID, "payload": request.Payload, "result": result})
	}
	listings := []map[string]any{}
	for _, request := range expected.Inbox.Listings {
		items := []inbox.Item{}
		offset := 0
		for {
			page, err := i.List(ctx, inbox.ListFilter{Project: request.Project, Limit: 100, Offset: offset})
			if err != nil {
				t.Fatal(err)
			}
			items = append(items, page.Items...)
			if len(items) > 100000 {
				t.Fatal("inbox row budget exceeded")
			}
			if page.NextOffset == nil {
				if int64(len(items)) != page.Total {
					t.Fatal("inbox total does not match paginated rows")
				}
				break
			}
			if *page.NextOffset <= offset {
				t.Fatal("inbox cursor did not advance")
			}
			offset = *page.NextOffset
		}
		events := []map[string]any{}
		after := int64(0)
		for {
			page, err := i.Events(ctx, request.Project, after, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			events = append(events, page...)
			if len(events) > 100000 {
				t.Fatal("event row budget exceeded")
			}
			sequence, ok := page[len(page)-1]["sequence"].(int64)
			if !ok || sequence <= after {
				t.Fatal("event cursor did not advance")
			}
			after = sequence
		}
		listings = append(listings, map[string]any{"project": request.Project, "items": items, "events": events})
	}
	answers := []map[string]any{}
	for _, request := range expected.Inbox.Answers {
		value, err := i.Answer(ctx, request.Project, request.ID, request.Payload, "migration_probe")
		var result any = value
		if err != nil {
			result = migrationError(err)
		}
		answers = append(answers, map[string]any{"project": request.Project, "id": request.ID, "request": request.Request, "payload": request.Payload, "result": result})
	}
	if err = u.Close(); err != nil {
		t.Fatal(err)
	}
	if err = i.Close(); err != nil {
		t.Fatal(err)
	}
	for _, store := range plan.Stores {
		digest, err := migrationHash(store.Archive, 128*1024*1024)
		if err != nil || digest != store.InitialSHA256 {
			t.Fatal("published legacy archive changed")
		}
	}
	result := map[string]any{"schema_version": 1, "clock": plan.Clock,
		"usage": map[string]any{"snapshots": snapshots, "history": history, "reset_receipts": resets},
		"inbox": map[string]any{"listings": listings, "answer_receipts": answers}}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 32*1024*1024 {
		t.Fatal("migration evidence exceeds 32 MiB")
	}
	f, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.Write(append(encoded, '\n')); err != nil {
		t.Fatal(err)
	}
	if err = f.Sync(); err != nil {
		t.Fatal(err)
	}
	t.Logf("legacy readers: usage snapshots=%d reset receipts=%d inbox project lists=%d answer receipts=%d; evidence=%s", len(snapshots), len(resets), len(listings), len(answers), filepath.Base(output))
}
