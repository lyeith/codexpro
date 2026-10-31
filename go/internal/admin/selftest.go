package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/files"
	gitservice "github.com/lyeith/codexpro/go/internal/git"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func (s *Service) selfTest(ctx context.Context, scope wire.Scope, a map[string]any) (wire.Result, error) {
	w, e := s.Workspaces.Resolve(ctx, scope, wire.String(a, "workspace_id", ""))
	if e != nil {
		return wire.Result{}, e
	}
	started := time.Now()
	checks := []check{}
	touched := []string{}
	probe := s.Config.String("contextDir", ".ai-bridge") + "/codexpro-self-test.md"
	add := func(name, status, detail string) {
		checks = append(checks, check{name, status, cleanOneLine(detail, 260)})
	}
	names := s.Registry.Names()
	expected := append([]string{}, names...)
	actual := append([]string{}, names...)
	if s.RegisteredNames != nil {
		actual = append([]string{}, s.RegisteredNames()...)
	}
	sort.Strings(expected)
	sort.Strings(actual)
	mode := s.Config.String("toolMode", "standard")
	auditMode := s.Config.String("auditMode", "metadata")
	session := s.Config.String("bashSessionId", "")
	requireSession := s.Config.Bool("requireBashSession", false)
	add("workspace", "pass", w.Root)
	state := "warn"
	if mode == "full" {
		state = "pass"
	}
	add("tool mode", state, fmt.Sprintf("%s; expected tools: %d", mode, len(expected)))
	add("write mode", "pass", "workspace")
	add("bash mode", "warn", "full")
	add("http auth", "pass", "token configured")
	missing, extra := []string{}, []string{}
	for _, name := range expected {
		if !contains(actual, name) {
			missing = append(missing, name)
		}
	}
	for _, name := range actual {
		if !contains(expected, name) {
			extra = append(extra, name)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		m, x := strings.Join(missing, ", "), strings.Join(extra, ", ")
		if m == "" {
			m = "none"
		}
		if x == "" {
			x = "none"
		}
		add("registered tool set", "fail", "missing: "+m+"; extra: "+x)
	} else {
		add("registered tool set", "pass", fmt.Sprintf("%d tools registered for %s mode", len(actual), mode))
	}
	skills, err := s.Files.DiscoverSkills(ctx, w, wire.Bool(a, "include_global_skills", true), max(1, min(120, wire.Int(a, "max_skills", 40))))
	if err != nil {
		add("inventory", "fail", selfErrorText(err))
	} else {
		servers := s.discoverMcpServers(w)
		add("inventory", "pass", fmt.Sprintf("%d skills inspected, %d MCP server names visible", len(skills), len(servers)))
	}
	status, err := s.Git.Status(ctx, w, "", false)
	if err != nil {
		add("git status", "fail", selfErrorText(err))
	} else if gitservice.IsFailure(status) {
		add("git status", "warn", status)
	} else {
		add("git status", "pass", fmt.Sprintf("%d changed entries", len(statusLines(status))))
	}
	if wire.Bool(a, "write_probe", false) {
		err = func() error {
			content := "# CodexPro Self Test\n\nUpdated: " + time.Now().UTC().Format("2006-01-02T15:04:05.000Z") + "\nWorkspace: " + w.Root + "\nmarker: before\n"
			args := map[string]any{"workspace_id": w.ID, "path": probe, "content": content, "create_dirs": true, "overwrite": true}
			if _, e := s.Files.Call(ctx, scope, "write", args); e != nil {
				return e
			}
			r, e := s.Files.Call(ctx, scope, "read", map[string]any{"workspace_id": w.ID, "path": probe, "max_bytes": 20000})
			if e != nil {
				return e
			}
			tag := wire.String(r.StructuredContent, "edit_tag", "")
			if _, e = s.Files.Call(ctx, scope, "edit", map[string]any{"workspace_id": w.ID, "path": probe, "edit_tag": tag, "edits": []any{map[string]any{"op": "replace", "start_line": 5, "content": "marker: after"}}}); e != nil {
				return e
			}
			r, e = s.Files.Call(ctx, scope, "read", map[string]any{"workspace_id": w.ID, "path": probe, "max_bytes": 20000})
			if e != nil {
				return e
			}
			if !strings.Contains(wire.String(r.StructuredContent, "text", ""), "marker: after") {
				return fmt.Errorf("self-test edit marker was not found after edit.")
			}
			st, e := s.Git.Status(ctx, w, probe, false)
			if e != nil {
				return e
			}
			lines := statusLines(st)
			touched = append(touched, probe)
			state := "warn"
			detail := "path-scoped status clean after write/edit"
			if len(lines) > 0 {
				detail = "path-scoped status: " + strings.Join(lines, ", ")
				all := true
				for _, line := range lines {
					all = all && strings.Contains(line, probe)
				}
				if all {
					state = "pass"
				}
			}
			add("write/edit probe", state, detail)
			return nil
		}()
		if err != nil {
			add("write/edit probe", "fail", selfErrorText(err))
		}
	} else {
		add("write/edit probe", "warn", "skipped by request")
	}
	if wire.Bool(a, "pro_context_probe", false) {
		if len(touched) == 0 {
			add("selected-only pro context", "warn", "skipped because write probe did not create the selected file")
		} else {
			selected, e := s.selectedContext(ctx, w, []string{probe}, 4, 80000)
			if e != nil {
				add("selected-only pro context", "fail", selfErrorText(e))
			} else if len(selected) == 1 && selected[0] == probe {
				add("selected-only pro context", "pass", "included only "+probe)
			} else {
				detail := strings.Join(selected, ", ")
				if detail == "" {
					detail = "no files"
				}
				add("selected-only pro context", "fail", "included "+detail)
			}
		}
	} else {
		add("selected-only pro context", "warn", "skipped by request")
	}
	if wire.Bool(a, "bash_probe", false) {
		result := files.RunCommand(ctx, w.Root, "nice", []string{"-n", "10", "bash", "-lc", "pwd"}, "", s.Config.Int("maxOutputBytes", 120000), 10*time.Second)
		if result.Err != nil {
			add("bash policy", "fail", selfErrorText(result.Err))
		} else {
			state := "fail"
			if result.ExitCode == 0 {
				state = "warn"
			}
			add("bash policy", state, "full bash is enabled; use only for trusted local repos")
		}
	} else {
		add("bash policy", "warn", "skipped by request")
	}
	add("terms boundary", "pass", "local workspace bridge only; does not provide models, proxy model access, bypass quotas, or execute remote/local agents from MCP")
	passed, warned, failed := 0, 0, 0
	for _, c := range checks {
		switch c.Status {
		case "pass":
			passed++
		case "warn":
			warned++
		case "fail":
			failed++
		}
	}
	overall := "pass"
	if warned > 0 {
		overall = "warn"
	}
	if failed > 0 {
		overall = "fail"
	}
	modeLine := fmt.Sprintf("Mode: tools=%s, write=workspace, handoff=off, debug_activity=%s, bash=full", mode, auditMode)
	if session != "" {
		modeLine += ", bash_session=" + session
		if requireSession {
			modeLine += " required"
		}
	}
	lines := []string{"# CodexPro Self Test", "", "Status: " + overall, "Workspace: " + w.Root, modeLine, fmt.Sprintf("Expected tools: %d", len(expected)), fmt.Sprintf("Registered tools: %d", len(actual)), fmt.Sprintf("Duration: %d ms", time.Since(started).Milliseconds()), "", "## Checks", ""}
	for _, c := range checks {
		lines = append(lines, "- "+strings.ToUpper(c.Status)+" "+c.Name+": "+c.Detail)
	}
	lines = append(lines, "", "## Terms Boundary", "", "CodexPro exposes local repo tools to the ChatGPT session the user controls. It does not provide models, proxy model access, resell access, modify quotas, bypass limits, or run local implementation agents through remote MCP tools.")
	return wire.Success("codexpro_self_test", "CodexPro Self Test", map[string]any{
		"workspace_id": w.ID, "root": w.Root, "status": overall, "passed": passed, "warned": warned, "failed": failed, "duration_ms": time.Since(started).Milliseconds(),
		"expected_tools": expected, "expected_tool_count": len(expected), "registered_tools": actual, "registered_tool_count": len(actual),
		"bash_mode": "full", "bash_session_id": nullable(session), "require_bash_session": requireSession, "write_mode": "workspace", "handoff_mode": "off", "debug_activity_mode": auditMode, "tool_mode": mode, "files_touched": touched, "checks": checks,
		"terms_boundary": map[string]any{"local_workspace_bridge": true, "provides_models": false, "proxies_model_access": false, "bypasses_quotas": false, "remote_agent_execution": false},
	}, strings.Join(lines, "\n")), nil
}
func statusLines(status string) []string {
	out := []string{}
	for _, v := range strings.Split(status, "\n") {
		v = files.TrimJS(v)
		if v != "" && v != "(no output)" && !strings.HasPrefix(v, "##") {
			out = append(out, v)
		}
	}
	return out
}

// The selected-only diagnostic remains available after handoff retirement. It
// builds its bounded selected-file context in memory and never publishes a bundle.
func (s *Service) selectedContext(ctx context.Context, w wire.Workspace, paths []string, maxFiles, maxBytes int) ([]string, error) {
	selected := []string{}
	var content strings.Builder
	content.WriteString("# CodexPro Self Test Context\n")
	for _, path := range paths {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if len(selected) >= maxFiles {
			break
		}
		abs, rel, e := s.Files.ResolvePath(w, path, false)
		if e != nil {
			return nil, e
		}
		b, e := files.ReadSource(abs, s.Config.Int("maxReadBytes", 180000))
		if e != nil {
			return nil, e
		}
		block := "\n## " + rel + "\n\n" + files.DecodeText(b) + "\n"
		if content.Len()+len(block) > maxBytes {
			continue
		}
		content.WriteString(block)
		selected = append(selected, rel)
	}
	return selected, nil
}

type mcpServer struct {
	Name   string
	Source string
}

var tomlServer = regexp.MustCompile(`(?m)^\s*\[(?:mcp_servers|mcpServers)\.(?:"([^"\].]+)"|([^"\].]+))\]\s*$`)

func (s *Service) discoverMcpServers(w wire.Workspace) []mcpServer {
	home, _ := os.UserHomeDir()
	home = s.Config.String("skillHomeDir", home)
	candidates := []struct{ path, kind, source string }{{filepath.Join(home, ".codex", "config.toml"), "toml", "user codex config"}, {filepath.Join(w.Root, ".mcp.json"), "json", "workspace config"}, {filepath.Join(w.Root, ".cursor", "mcp.json"), "json", "workspace cursor config"}, {filepath.Join(home, ".cursor", "mcp.json"), "json", "user cursor config"}}
	out := []mcpServer{}
	seen := map[string]bool{}
	add := func(name, source string) {
		key := source + ":" + name
		if !seen[key] {
			seen[key] = true
			out = append(out, mcpServer{name, source})
		}
	}
	for _, c := range candidates {
		f, e := os.Open(c.path)
		if e != nil {
			continue
		}
		b, e := io.ReadAll(io.LimitReader(f, 200000))
		_ = f.Close()
		if e != nil {
			continue
		}
		if c.kind == "toml" {
			for _, m := range tomlServer.FindAllStringSubmatch(files.DecodeText(b), -1) {
				name := m[1]
				if name == "" {
					name = m[2]
				}
				add(name, c.source)
			}
		} else {
			var root map[string]json.RawMessage
			if json.Unmarshal(b, &root) != nil {
				continue
			}
			var servers map[string]json.RawMessage
			if json.Unmarshal(root["mcpServers"], &servers) != nil {
				continue
			}
			for name := range servers {
				add(name, c.source)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		v := files.Compare(out[i].Name, out[j].Name)
		if v == 0 {
			return files.Compare(out[i].Source, out[j].Source) < 0
		}
		return v < 0
	})
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

func selfErrorText(err error) string {
	result := wire.ErrorResult(err)
	return wire.String(result.StructuredContent, "error", err.Error())
}
