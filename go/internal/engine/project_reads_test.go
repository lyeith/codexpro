package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

type personalReadFixture struct {
	engine              *Engine
	scope               wire.Scope
	parent, alpha, beta string
	fixtureToken        string
}

func newPersonalReadFixture(t *testing.T) personalReadFixture {
	t.Helper()
	t.Setenv("CODEXPRO_JOB_SCOPES", "0")
	dir := t.TempDir()
	parent := filepath.Join(dir, "collection")
	f := personalReadFixture{
		parent: parent, alpha: filepath.Join(parent, "repo-a"), beta: filepath.Join(parent, "repo-b"),
		fixtureToken: "pa1_" + strings.Repeat("A", 43),
		scope: wire.Scope{
			PrincipalID: "principal:reader", SessionID: "read-session",
			Identity: wire.Identity{Source: "named", PrincipalType: "person", PrincipalID: "reader",
				Role: "tester", CredentialID: "private-device", AgentID: "registered-crew", AgentKind: "crew"},
		},
	}
	projects := []wire.Project{
		{ID: "collection", Label: "Parent catalog", Root: f.parent},
		{ID: "alpha", Label: "Granted Alpha", Root: f.alpha},
		{ID: "beta", Label: "Foreign Beta", Root: f.beta},
	}
	for _, p := range projects {
		if err := os.Mkdir(p.Root, 0700); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "init", "-q", "--template=", p.Root).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
	}
	cfg := config.New(filepath.Join(dir, "state"))
	if err := os.MkdirAll(cfg.Home, 0700); err != nil {
		t.Fatal(err)
	}
	catalog, err := json.Marshal(workspace.Catalog{Version: 1, DefaultProject: "beta", Projects: projects})
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(cfg.ProjectsFile, string(catalog))
	write(filepath.Join(f.alpha, "public.txt"), "literal[1] "+f.fixtureToken+"\n")
	write(filepath.Join(f.beta, "public.txt"), "foreign-receipt-source\n")
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 1))); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(f.beta, "sample.png"), picture.String())
	write(filepath.Join(f.alpha, "fake.png"), picture.String()[:24]+f.fixtureToken)
	write(filepath.Join(f.alpha, "sample.png"), picture.String()+f.fixtureToken)
	secret := filepath.Join(f.beta, "private.txt")
	write(secret, "foreign-only-marker\n")
	if err = os.Symlink(secret, filepath.Join(f.alpha, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(secret, filepath.Join(f.alpha, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	f.engine, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.engine.Close() })
	return f
}

func (f personalReadFixture) ctx(projects ...string) context.Context {
	return wire.WithProjects(wire.WithIdentity(context.Background(), f.scope.Identity), projects)
}

func (f personalReadFixture) call(t *testing.T, ctx context.Context, tool string, args map[string]any, wantError bool) wire.Result {
	t.Helper()
	r, err := f.engine.Call(ctx, f.scope, tool, args)
	if err != nil || r.IsError != wantError {
		t.Fatalf("%s: error=%t, want %t (transport error %v)", tool, r.IsError, wantError, err)
	}
	return r
}

func renderPersonalResult(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestProjectReadGrantedWorkspaceSurfaces(t *testing.T) {
	f := newPersonalReadFixture(t)
	ctx := f.ctx("alpha", "collection")
	a, b := workspace.IDForRoot(f.alpha), workspace.IDForRoot(f.beta)
	// Arrange an existing job for the same registration before removing its beta grant.
	// The ordinary list_projects completion digest would advertise this job without a workspace filter.
	_, err := f.engine.Jobs.Call(f.ctx("beta"), f.scope, "start_jobs", map[string]any{
		"workspace_id": b, "commands": []any{map[string]any{"command": "printf foreign-job-marker"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs := f.engine.Jobs.Snapshot()
	if len(jobs) != 1 || jobs[0].Acknowledged {
		t.Fatal("fixture must contain an unacknowledged job")
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"list_projects", nil, a},
		{"tree", map[string]any{"workspace_id": a, "max_depth": 1}, "public.txt"},
		{"read", map[string]any{"workspace_id": a, "path": "public.txt"}, "literal[1]"},
		{"view_image", map[string]any{"workspace_id": a, "path": "sample.png"}, "image/png"},
		{"search", map[string]any{"workspace_id": a, "query": "literal[1]"}, "public.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := f.call(t, ctx, tc.name, tc.args, false)
			shown := renderPersonalResult(t, r)
			if !strings.Contains(shown, tc.want) {
				t.Fatalf("missing %q in %s", tc.want, shown)
			}
			for _, hidden := range []string{f.parent, "Foreign Beta", "Parent catalog", b, "foreign-only-marker",
				jobs[0].ID, "background_jobs", "recent_activity", "foreign-job-marker", f.fixtureToken, f.scope.Identity.CredentialID} {
				if strings.Contains(shown, hidden) {
					t.Fatalf("restricted %s exposed a hidden field or value", tc.name)
				}
			}
			if tc.name == "view_image" {
				var output []byte
				for _, item := range r.Content {
					if item["type"] == "image" {
						data, ok := item["data"].(string)
						if !ok {
							t.Fatal("image block lacks payload")
						}
						output, err = base64.StdEncoding.DecodeString(data)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				projected, _, err := image.Decode(bytes.NewReader(output))
				if err != nil || projected.Bounds() != image.Rect(0, 0, 2, 1) {
					t.Fatal("could not decode the granted pixels")
				}
				if bytes.Contains(output, []byte(f.fixtureToken)) {
					t.Fatal("image block included a synthetic credential after the image stream")
				}
			}
			if tc.name == "list_projects" && r.StructuredContent["count"] != 1 {
				t.Fatalf("unexpected project count: %v", r.StructuredContent["count"])
			}
		})
	}
	if f.engine.Jobs.Snapshot()[0].Acknowledged {
		t.Fatal("restricted read acknowledged the prior job")
	}
	// Match presence alone must not reveal a secret prefix or an aliased file.
	for _, query := range []string{"foreign-only-marker", f.fixtureToken[:12]} {
		r := f.call(t, ctx, "search", map[string]any{"workspace_id": a, "query": query}, false)
		if renderPersonalResult(t, r.StructuredContent["matches"]) != "[]" {
			t.Fatal("search matched outside the redacted, granted corpus")
		}
	}
}

func TestProjectReadRefusalsHaveNoSideEffects(t *testing.T) {
	f := newPersonalReadFixture(t)
	a, b, p := workspace.IDForRoot(f.alpha), workspace.IDForRoot(f.beta), workspace.IDForRoot(f.parent)
	ctx := f.ctx("alpha", "collection")
	for _, tc := range []struct {
		label, tool string
		args        map[string]any
	}{
		{"foreign", "read", map[string]any{"workspace_id": b, "path": "public.txt"}},
		{"aggregate", "tree", map[string]any{"workspace_id": p}},
		{"foreign-image", "view_image", map[string]any{"workspace_id": b, "path": "sample.png"}},
		{"malformed-image", "view_image", map[string]any{"workspace_id": a, "path": "fake.png"}},
		{"missing-workspace", "read", map[string]any{"path": "public.txt"}},
		{"unknown-workspace", "tree", map[string]any{"workspace_id": "ws_000000000000000000000000"}},
		{"absolute", "read", map[string]any{"workspace_id": a, "path": filepath.Join(f.beta, "public.txt")}},
		{"traversal", "search", map[string]any{"workspace_id": a, "path": "../repo-b", "query": "foreign"}},
		{"symlink", "read", map[string]any{"workspace_id": a, "path": "linked.txt"}},
		{"hardlink", "read", map[string]any{"workspace_id": a, "path": "alias.txt"}},
		{"hidden", "read", map[string]any{"workspace_id": a, "path": ".git/config"}},
		{"symbol-search", "search", map[string]any{"workspace_id": a, "kind": "symbol", "query": "literal"}},
		{"diff-search", "search", map[string]any{"workspace_id": a, "scope": "diff_added", "query": "literal"}},
		{"foreign-list", "activity_list", map[string]any{"project_id": "beta"}},
		{"mixed-list", "activity_list", map[string]any{"project_id": "alpha", "workspace_id": b}},
		{"global-list", "activity_list", nil},
		{"global-status", "activity_status", nil},
		{"direct-bash", "bash", map[string]any{"agent": "SP1", "workspace_id": a, "command": "touch blocked-marker"}},
		{"direct-write", "write", map[string]any{"workspace_id": a, "path": "blocked-marker", "content": "forbidden"}},
		{"outer-batch", "batch", map[string]any{
			"workspace_id": a, "operations": []any{map[string]any{"id": "one", "tool": "bash", "args": map[string]any{"command": "touch blocked-marker"}}},
		}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			r := f.call(t, ctx, tc.tool, tc.args, true)
			if r.StructuredContent["error_code"] != "access_denied" {
				t.Fatal("nonuniform refusal")
			}
		})
	}
	f.call(t, f.ctx(), "read", map[string]any{"workspace_id": a, "path": "public.txt"}, true)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bash", map[string]any{"workspace_id": a, "command": "touch blocked-marker"}},
		{"write", map[string]any{"workspace_id": a, "path": "blocked-marker", "content": "forbidden"}},
	} {
		_, err := f.engine.CallBatchChild(ctx, f.scope, tc.name, tc.args)
		if denied, ok := err.(*wire.Error); !ok || denied.Code != "access_denied" {
			t.Fatalf("%s child refusal: %v", tc.name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.alpha, "blocked-marker")); !os.IsNotExist(err) {
		t.Fatalf("refused mutation affected workspace: %v", err)
	}
}

func TestProjectReadReceiptsFollowCurrentGrants(t *testing.T) {
	f := newPersonalReadFixture(t)
	ids := map[string]string{}
	for _, p := range []struct{ id, root string }{{"alpha", f.alpha}, {"beta", f.beta}} {
		ctx := f.ctx(p.id)
		ws := workspace.IDForRoot(p.root)
		f.call(t, ctx, "read", map[string]any{"workspace_id": ws, "path": "public.txt"}, false)
		list := f.call(t, ctx, "activity_list", map[string]any{"project_id": p.id, "workspace_id": ws}, false)
		var actions []map[string]any
		if err := json.Unmarshal([]byte(renderPersonalResult(t, list.StructuredContent["actions"])), &actions); err != nil {
			t.Fatal(err)
		}
		if len(actions) != 1 {
			t.Fatalf("%s: expected one receipt, got %d", p.id, len(actions))
		}
		receipt := actions[0]
		for k := range receipt {
			if !slices.Contains([]string{"action_id", "project_id", "occurred_at", "tool_name", "status"}, k) {
				t.Fatalf("operator field %q in reader receipt", k)
			}
		}
		id, _ := receipt["action_id"].(string)
		if id == "" || len(receipt) != 5 || receipt["project_id"] != p.id || receipt["tool_name"] != "read" {
			t.Fatalf("incomplete %s receipt", p.id)
		}
		ids[p.id] = id
		get := f.call(t, ctx, "activity_get", map[string]any{"action_id": id}, false)
		if len(list.StructuredContent) != 6 || list.StructuredContent["project_id"] != p.id || len(get.StructuredContent) != 3 {
			t.Fatal("unexpected top-level receipt fields")
		}
		if renderPersonalResult(t, list.StructuredContent["has_more"]) != "false" || list.StructuredContent["truncated"] != false {
			t.Fatalf("incorrect completeness for exhausted project: %#v", list.StructuredContent)
		}
		if renderPersonalResult(t, get.StructuredContent["action"]) != renderPersonalResult(t, receipt) {
			t.Fatal("detail exposed a different projection than list")
		}
		for _, response := range []wire.Result{list, get} {
			shown := renderPersonalResult(t, response)
			for _, hidden := range []string{f.parent, "public.txt", f.scope.Identity.CredentialID, "foreign-receipt-source", "literal[1]", "recent_activity", "background_jobs"} {
				if strings.Contains(shown, hidden) {
					t.Fatal("receipt response exposed private detail")
				}
			}
		}
	}
	if ids["alpha"] == ids["beta"] {
		t.Fatal("fixture reads must have distinct receipts")
	}
	ctx := f.ctx("alpha")
	foreign := f.call(t, ctx, "activity_get", map[string]any{"action_id": ids["beta"]}, true)
	missing := f.call(t, ctx, "activity_get", map[string]any{"action_id": "cpa_" + strings.Repeat("0", 32)}, true)
	if renderPersonalResult(t, foreign) != renderPersonalResult(t, missing) {
		t.Fatal("distinguishable foreign and missing receipt responses")
	}
	revoked := f.call(t, f.ctx(), "activity_get", map[string]any{"action_id": ids["alpha"]}, true)
	if renderPersonalResult(t, revoked) != renderPersonalResult(t, missing) {
		t.Fatal("revoked grant disclosed receipt existence")
	}
}
