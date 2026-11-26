//go:build linux

package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/admin"
	"github.com/lyeith/codexpro/go/internal/batch"
	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/engine"
	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Real queued jobs execute this private test binary through the normal runner
// entry point, just as the engine and jobs integration fixtures do.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "runner" {
		if err := process.RunRunner(os.Args[2:]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type executionMCPBearer struct {
	base  http.RoundTripper
	token string
}

func (a executionMCPBearer) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+a.token)
	return a.base.RoundTrip(request)
}

type executionMCPCaller struct {
	engine        *engine.Engine
	mu            sync.Mutex
	calls         []wire.Scope
	children      []wire.Scope
	blockWrites   bool
	entered       chan wire.Scope
	proceed       chan struct{}
	unblock       sync.Once
	block         sync.Once
	childReleased chan struct{}
	released      sync.Once
}

func (c *executionMCPCaller) Call(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	if wire.IdentityFrom(ctx) != scope.Identity {
		return wire.Result{}, fmt.Errorf("transport lost its authenticated scope")
	}
	c.mu.Lock()
	c.calls = append(c.calls, scope)
	c.mu.Unlock()
	return c.engine.Call(ctx, scope, name, args)
}

func (c *executionMCPCaller) CallBatchChild(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	if wire.IdentityFrom(ctx) != scope.Identity {
		return wire.Result{}, fmt.Errorf("batch child lost its authenticated scope")
	}
	c.mu.Lock()
	c.children = append(c.children, scope)
	c.mu.Unlock()
	if c.blockWrites && name == "write" {
		c.block.Do(func() {
			c.entered <- scope
			<-c.proceed
		})
	}
	return c.engine.CallBatchChild(ctx, scope, name, args)
}

func (c *executionMCPCaller) RetainBatchChild(ctx context.Context) (context.Context, func(), error) {
	retained, release, err := c.engine.RetainBatchChild(ctx)
	if err != nil {
		return retained, release, err
	}
	return retained, func() {
		release()
		c.released.Do(func() { close(c.childReleased) })
	}, nil
}

func (c *executionMCPCaller) release() { c.unblock.Do(func() { close(c.proceed) }) }

type executionMCPFixture struct {
	engine *engine.Engine
	caller *executionMCPCaller
	server *Server
	remote *httptest.Server
	root   string
	id     string
	tokens []string
}

func newExecutionMCPFixture(t *testing.T, blockWrites bool) *executionMCPFixture {
	t.Helper()
	// This integration exercises real private process groups. External owner-unit
	// activation and whole-cgroup proof belong to the separate owner acceptance.
	t.Setenv("CODEXPRO_JOB_SCOPES", "0")
	base := t.TempDir()
	root, home := filepath.Join(base, "project"), filepath.Join(base, "state")
	for _, path := range []string{root, home} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	policyPath, policy, tokens := namedPolicy(t)
	policy.Principals[0].Type, policy.Principals[0].Role = "system", ""
	policy.Agents[0].Kind, policy.Agents[0].Address = "leaf", "system/alpha"
	policy.Principals[1].Projects = []string{"fixture"}
	savePolicy(t, policyPath, policy)
	cfg := config.New(home)
	cfg.Set("principalsFile", policyPath)
	cfg.Set("jobTimeoutMs", 15000)
	cfg.Set("bashTimeoutMs", 5000)
	cfg.Set("maxBashTimeoutMs", 15000)
	catalog, err := json.Marshal(workspace.Catalog{
		Version: 1, DefaultProject: "fixture",
		Projects: []wire.Project{{ID: "fixture", Root: root, Label: "Execution fixture", BaseRef: "HEAD"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.ProjectsFile, catalog, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "readable.txt"), []byte("authorized inspection\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e, err := engine.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	caller := &executionMCPCaller{
		engine: e, blockWrites: blockWrites, entered: make(chan wire.Scope, 1),
		proceed: make(chan struct{}), childReleased: make(chan struct{}),
	}
	service, err := batch.New(cfg, e.Workspaces, caller, func(name string, args map[string]any) (map[string]any, error) {
		parsed, err := e.Registry.ValidateInner(name, args)
		if validation, ok := err.(*contracts.ValidationError); ok {
			err = validation.BusinessError()
		}
		return parsed, err
	})
	if err != nil {
		t.Fatal(err)
	}
	service.SetFiles(e.Files)
	e.Batch = service
	e.Admin, err = admin.New(cfg, e.Workspaces, e.Files, e.Git, e.Registry, e.Audit)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(cfg, e.Registry, caller, "execution-fixture", e.DropSession)
	if err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(server)
	t.Cleanup(func() { caller.release(); _ = server.Close(); remote.Close() })
	return &executionMCPFixture{engine: e, caller: caller, server: server, remote: remote, root: root, id: workspace.IDForRoot(root), tokens: tokens}
}

func (f *executionMCPFixture) connect(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "execution-hold-fixture", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: f.remote.URL + "/mcp",
		HTTPClient: &http.Client{Transport: executionMCPBearer{
			base: f.remote.Client().Transport, token: token,
		}, Timeout: 15 * time.Second},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if !sessionPattern.MatchString(session.ID()) {
		t.Fatal("SDK did not establish a stateful MCP session")
	}
	return session
}

func executionMCPCall(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	originalSession := session.ID()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s protocol failure: %v", name, err)
	}
	if session.ID() != originalSession {
		t.Fatal("tool call replaced the established SDK session")
	}
	return result
}

func executionMCPData(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	data, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("missing structured tool result: %#v", result)
	}
	return data
}

func executionMCPHeld(t *testing.T, result *mcp.CallToolResult) {
	t.Helper()
	if !result.IsError || executionMCPData(t, result)["error_code"] != "execution_held" {
		t.Fatalf("fresh SDK call was not explicitly held: %#v", result)
	}
}

func executionMCPAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unexpected effect at %s: %v", path, err)
	}
}

func executionMCPWitness(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "helper-or-owner-called")
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
	body := fmt.Sprintf("#!/bin/sh\nprintf 'called\\n' >> %s\nexit 99\n", quoted)
	for _, name := range []string{"git", "nice", "bash", "ssd-dev", "systemd-run"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

func executionMCPWait(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for " + what)
	}
}

func TestExecutionHoldEstablishedSDKSessionKeepsOriginalWork(t *testing.T) {
	f := newExecutionMCPFixture(t, false)
	session := f.connect(t, f.tokens[0])
	reader := f.connect(t, f.tokens[2])
	originalSession := session.ID()
	// A private loopback handshake places the original runner behind a precise
	// release boundary. No duration guess decides whether it was admitted.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan net.Conn, 1)
	failed := make(chan error, 1)
	accepted := make(chan struct{})
	var gate net.Conn
	gateCtx, stopGate := context.WithCancel(context.Background())
	t.Cleanup(func() {
		stopGate()
		_ = listener.Close()
		if gate != nil {
			_ = gate.Close()
		}
		executionMCPWait(t, accepted, "private runner handshake cleanup")
		select {
		case conn := <-ready:
			_ = conn.Close()
		default:
		}
	})
	go func() {
		defer close(accepted)
		conn, err := listener.Accept()
		if err != nil {
			failed <- err
			return
		}
		stopClose := context.AfterFunc(gateCtx, func() { _ = conn.Close() })
		defer stopClose()
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil || line != "original-ready\n" {
			_ = conn.Close()
			failed <- fmt.Errorf("original handshake %q: %v", line, err)
			return
		}
		ready <- conn
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	command := fmt.Sprintf("exec 3<>/dev/tcp/127.0.0.1/%d; printf 'original-ready\\n' >&3; IFS= read -r value <&3 && test \"$value\" = continue && printf 'once\\n' >> original-once && printf original-output", port)
	args := map[string]any{
		"workspace_id": f.id, "queue_key": "sdk-original", "agent": "FP5",
		"commands": []any{map[string]any{"command": command}},
	}
	first := executionMCPCall(t, session, "queue_jobs", args)
	if first.IsError {
		t.Fatalf("original queue submission failed: %#v", first)
	}
	ids := executionMCPData(t, first)["job_ids"]
	select {
	case gate = <-ready:
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("original accepted runner did not reach the release boundary")
	}
	original := f.engine.Jobs.Snapshot()
	if len(original) != 1 || original[0].Status != "running" || original[0].Identity.PrincipalID != "alpha" || original[0].Identity.AgentID != "crew-alpha" {
		t.Fatalf("original admission lost its identity: %+v", original)
	}
	hold, err := f.engine.Jobs.BeginExecutionHold("sdk-held")
	if err != nil || !hold.AdmissionHeld {
		t.Fatalf("hold ingress: %+v %v", hold, err)
	}
	if status, err := f.engine.Jobs.HoldExecutionDispatch(hold.HoldID); err != nil || status.DispatchHeld || len(status.OutstandingJobs) != 1 {
		t.Fatalf("running original was hidden by the hold: %+v %v", status, err)
	}
	oldPath := os.Getenv("PATH")
	witness := executionMCPWitness(t)
	for _, item := range []struct {
		tool string
		args map[string]any
	}{
		{"bash", map[string]any{"workspace_id": f.id, "command": "printf forbidden > forbidden"}},
		{"batch", map[string]any{"workspace_id": f.id, "operations": []any{map[string]any{"tool": "write", "args": map[string]any{"path": "forbidden", "content": "forbidden"}}}}},
		{"codexpro_self_test", map[string]any{"workspace_id": f.id, "write_probe": true, "bash_probe": true}},
		{"queue_jobs", map[string]any{"workspace_id": f.id, "queue_key": "new-sdk-work", "commands": []any{map[string]any{"command": "printf forbidden"}}}},
	} {
		executionMCPHeld(t, executionMCPCall(t, session, item.tool, item.args))
	}
	retry := executionMCPCall(t, session, "queue_jobs", args)
	if retry.IsError || !reflect.DeepEqual(executionMCPData(t, retry)["job_ids"], ids) {
		t.Fatalf("held SDK retry lost the exact original receipt: %#v", retry)
	}
	alias := executionMCPCall(t, session, "start_jobs", args)
	encoded, _ := json.Marshal(alias)
	if !alias.IsError || alias.StructuredContent != nil || !strings.Contains(string(encoded), "queue_jobs") {
		t.Fatalf("typed queue alias validation changed: %s", encoded)
	}
	executionMCPAbsent(t, witness)
	executionMCPAbsent(t, filepath.Join(f.root, "forbidden"))
	executionMCPAbsent(t, filepath.Join(f.root, batch.StoreDir))
	afterRefusals := f.engine.Jobs.Snapshot()
	if len(afterRefusals) != 1 || afterRefusals[0].ID != original[0].ID ||
		afterRefusals[0].DeadlineMS != original[0].DeadlineMS || afterRefusals[0].TimeoutMS != original[0].TimeoutMS ||
		afterRefusals[0].ScopeUnit != original[0].ScopeUnit || !reflect.DeepEqual(afterRefusals[0].ResourceClaim, original[0].ResourceClaim) {
		t.Fatalf("refusals changed original work or allocated another owner: %+v", afterRefusals)
	}
	t.Setenv("PATH", oldPath)
	for _, tool := range []string{"read", "server_config", "jobs"} {
		callArgs := map[string]any{}
		switch tool {
		case "read":
			callArgs = map[string]any{"workspace_id": f.id, "path": "readable.txt"}
		case "jobs":
			callArgs = map[string]any{"workspace_id": f.id, "job_ids": ids, "output": "none", "wait_ms": 0}
		}
		if result := executionMCPCall(t, session, tool, callArgs); result.IsError {
			t.Fatalf("held session lost %s: %#v", tool, result)
		}
	}
	if result := executionMCPCall(t, reader, "read", map[string]any{"workspace_id": f.id, "path": "readable.txt"}); result.IsError {
		t.Fatal("authorized personal inspection was blocked by the hold")
	}
	f.caller.mu.Lock()
	callsBeforeDenial := len(f.caller.calls)
	f.caller.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	denied, deniedErr := reader.CallTool(ctx, &mcp.CallToolParams{Name: "batch", Arguments: map[string]any{
		"workspace_id": f.id, "operations": []any{map[string]any{"tool": "write", "args": map[string]any{"path": "foreign", "content": "forbidden"}}},
	}})
	cancel()
	if denied != nil || deniedErr == nil || !strings.Contains(deniedErr.Error(), "tool is unavailable for this credential") {
		t.Fatalf("personal batch did not receive the credential authorization refusal: result=%#v error=%v", denied, deniedErr)
	}
	f.caller.mu.Lock()
	callsAfterDenial := len(f.caller.calls)
	children := len(f.caller.children)
	f.caller.mu.Unlock()
	if callsAfterDenial != callsBeforeDenial {
		t.Fatalf("credential refusal reached the caller: calls before=%d after=%d", callsBeforeDenial, callsAfterDenial)
	}
	if children != 0 {
		t.Fatalf("a refused SDK batch dispatched %d children", children)
	}
	executionMCPAbsent(t, filepath.Join(f.root, "foreign"))
	if _, err := io.WriteString(gate, "continue\n"); err != nil {
		t.Fatal(err)
	}
	_ = gate.Close()
	collected := executionMCPCall(t, session, "jobs", map[string]any{"workspace_id": f.id, "job_ids": ids, "output": "head", "wait_ms": 5000})
	data := executionMCPData(t, collected)
	rows, ok := data["jobs"].([]any)
	if collected.IsError || data["all_succeeded"] != true || !ok || len(rows) != 1 ||
		rows[0].(map[string]any)["job_id"] != original[0].ID || rows[0].(map[string]any)["stdout"] != "original-output" {
		t.Fatalf("original SDK result was not preserved: %#v", collected)
	}
	bytes, err := os.ReadFile(filepath.Join(f.root, "original-once"))
	if err != nil || string(bytes) != "once\n" {
		t.Fatalf("accepted command did not finish exactly once: %q %v", bytes, err)
	}
	finishedRetry := executionMCPCall(t, session, "queue_jobs", args)
	if finishedRetry.IsError || !reflect.DeepEqual(executionMCPData(t, finishedRetry)["job_ids"], ids) || len(f.engine.Jobs.Snapshot()) != 1 {
		t.Fatal("terminal receipt retry created another submission")
	}
	if status, err := f.engine.Jobs.HoldExecutionDispatch(hold.HoldID); err != nil || !status.DispatchHeld {
		t.Fatalf("completed original did not settle: %+v %v", status, err)
	}
	if session.ID() != originalSession || f.server.SessionCount() != 2 {
		t.Fatal("hold closed or replaced an established MCP session")
	}
	f.caller.mu.Lock()
	defer f.caller.mu.Unlock()
	for _, scope := range f.caller.calls {
		if scope.Identity.PrincipalID == "alpha" && (scope.SessionID != originalSession || scope.Identity.AgentID != "crew-alpha") {
			t.Fatalf("session or child principal binding changed: %+v", scope)
		}
	}
}

func TestExecutionHoldEstablishedSDKBatchContinuation(t *testing.T) {
	f := newExecutionMCPFixture(t, true)
	session := f.connect(t, f.tokens[0])
	originalSession := session.ID()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type outcome struct {
		result *mcp.CallToolResult
		err    error
	}
	result := make(chan outcome, 1)
	done := make(chan struct{})
	t.Cleanup(func() { f.caller.release(); executionMCPWait(t, done, "original SDK batch cleanup") })
	go func() {
		defer close(done)
		value, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "batch", Arguments: map[string]any{
			"workspace_id": f.id, "persist": false,
			"operations": []any{
				map[string]any{"id": "write", "tool": "write", "args": map[string]any{"path": "once.txt", "content": "original batch\n"}},
				map[string]any{"id": "read", "tool": "read", "args": map[string]any{"path": "once.txt"}},
			},
		}})
		result <- outcome{value, err}
	}()
	var admitted wire.Scope
	select {
	case admitted = <-f.caller.entered:
	case <-ctx.Done():
		t.Fatal("original SDK batch did not reach its child")
	}
	hold, err := f.engine.Jobs.BeginExecutionHold("sdk-original-batch")
	if err != nil {
		t.Fatal(err)
	}
	if status, err := f.engine.Jobs.HoldExecutionDispatch(hold.HoldID); err != nil || status.DispatchHeld || len(status.PendingCalls) == 0 {
		t.Fatalf("hold lost the admitted SDK call: %+v %v", status, err)
	}
	executionMCPHeld(t, executionMCPCall(t, session, "batch", map[string]any{
		"workspace_id": f.id, "persist": false,
		"operations": []any{map[string]any{"tool": "write", "args": map[string]any{"path": "new.txt", "content": "forbidden"}}},
	}))
	executionMCPAbsent(t, filepath.Join(f.root, "new.txt"))
	f.caller.release()
	var original outcome
	select {
	case original = <-result:
	case <-ctx.Done():
		t.Fatal("admitted SDK batch did not finish")
	}
	if original.err != nil || original.result == nil || original.result.IsError {
		t.Fatalf("admitted SDK batch lost its continuation: %v %#v", original.err, original.result)
	}
	executionMCPWait(t, f.caller.childReleased, "original read-child release")
	contents, err := os.ReadFile(filepath.Join(f.root, "once.txt"))
	if err != nil || string(contents) != "original batch\n" {
		t.Fatalf("original batch result changed: %q %v", contents, err)
	}
	f.caller.mu.Lock()
	children := append([]wire.Scope{}, f.caller.children...)
	f.caller.mu.Unlock()
	if len(children) != 2 {
		t.Fatalf("original children ran %d times", len(children))
	}
	for _, child := range children {
		if child.Identity != admitted.Identity || child.Identity.PrincipalID != "alpha" ||
			child.SessionID != originalSession || child.ParentActionID != admitted.ParentActionID {
			t.Fatalf("batch child changed authenticated parent scope: %+v", child)
		}
	}
	if session.ID() != originalSession || len(f.engine.Jobs.Snapshot()) != 0 {
		t.Fatal("batch continuation replaced its session or allocated a job")
	}
	if status, err := f.engine.Jobs.HoldExecutionDispatch(hold.HoldID); err != nil || !status.DispatchHeld {
		t.Fatalf("finished SDK batch retained execution admission: %+v %v", status, err)
	}
}
