// mcp-smoke checks a running CodexPro endpoint after a release switch. It is
// read-only: health, tools/list, list_projects, a stale-session call, GET /mcp
// and, in stateless mode, 2026-07-28 server/discover. It never prints the token.
//
//	go run ./cmd/mcp-smoke -url https://host/mcp -token-file ~/.config/codexpro/http-token \
//	    -mode stateless -projects rdp-platform,press -batch-max 24
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const staleSession = "6f1c2a4e-9b7d-4c3e-8a21-5d0f7e9b1c33"

type smoke struct {
	endpoint, token string
	client          *http.Client
	session         string // stateful mode only
	failed          bool
}

func main() {
	endpoint := flag.String("url", "http://127.0.0.1:7200/mcp", "MCP endpoint")
	tokenFile := flag.String("token-file", os.ExpandEnv("$HOME/.config/codexpro/http-token"), "bearer token file")
	mode := flag.String("mode", "stateless", "expected httpSessionMode")
	revision := flag.String("revision", "", "expected /healthz revision prefix (optional)")
	projects := flag.String("projects", "", "comma-separated project ids that list_projects must include")
	batchMax := flag.Int("batch-max", 0, "expected batch operations maxItems (0 skips)")
	flag.Parse()
	raw, err := os.ReadFile(*tokenFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "token file:", err)
		os.Exit(2)
	}
	s := &smoke{endpoint: *endpoint, token: strings.TrimSpace(string(raw)), client: &http.Client{Timeout: 30 * time.Second}}

	health := map[string]any{}
	status, _, body, err := s.do("GET", strings.TrimSuffix(*endpoint, "/mcp")+"/healthz", "", nil)
	if err == nil && status == 200 {
		err = json.Unmarshal(body, &health)
	}
	s.check("healthz", err == nil && status == 200 && health["httpSessionMode"] == *mode &&
		strings.HasPrefix(fmt.Sprint(health["revision"]), *revision),
		"status=%d revision=%v httpSessionMode=%v %v", status, health["revision"], health["httpSessionMode"], err)

	if *mode == "stateless" {
		// No initialize: a sessionless client may call directly.
		s.toolsList(*batchMax, "")
	} else {
		s.rpc("initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"mcp-smoke","version":"1"}}}`, "", nil)
		s.check("session issued", s.session != "", "%q", s.session)
		_, _, _, _ = s.do("POST", s.endpoint, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, func(r *http.Request) {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", "application/json, text/event-stream")
			r.Header.Set("Mcp-Session-Id", s.session)
		})
		s.toolsList(*batchMax, "")
	}
	result, ok := s.rpc("list_projects", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_projects","arguments":{}}}`, "", nil)
	if ok {
		ids := map[string]bool{}
		structured, _ := result["structuredContent"].(map[string]any)
		list, _ := structured["projects"].([]any)
		for _, p := range list {
			if m, ok := p.(map[string]any); ok {
				ids[fmt.Sprint(m["id"])] = true
			}
		}
		missing := []string{}
		for _, id := range strings.Split(*projects, ",") {
			if id = strings.TrimSpace(id); id != "" && !ids[id] {
				missing = append(missing, id)
			}
		}
		s.check("list_projects ids", len(list) > 0 && len(missing) == 0, "%d projects, missing %v", len(list), missing)
	}
	if *mode == "stateless" {
		s.rpc("stale session call", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_projects","arguments":{}}}`, staleSession, nil)
		status, header, _, err := s.do("GET", *endpoint, "", nil)
		s.check("GET /mcp", err == nil && status == 405 && header.Get("Allow") == "POST", "status=%d Allow=%q %v", status, header.Get("Allow"), err)
		meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"mcp-smoke","version":"1"}}`
		discovered, ok := s.rpc("server/discover", `{"jsonrpc":"2.0","id":5,"method":"server/discover","params":{`+meta+`}}`, "",
			map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "server/discover"})
		if ok {
			versions, _ := discovered["supportedVersions"].([]any)
			s.check("server/discover versions", len(versions) > 0 && versions[0] == "2026-07-28", "%v", versions)
		}
	}
	if s.failed {
		os.Exit(1)
	}
	fmt.Println("mcp-smoke: all checks passed")
}

func (s *smoke) check(name string, ok bool, format string, args ...any) {
	state := "ok  "
	if !ok {
		state, s.failed = "FAIL", true
	}
	fmt.Printf("%s %-26s %s\n", state, name, fmt.Sprintf(format, args...))
}

func (s *smoke) toolsList(batchMax int, session string) {
	result, ok := s.rpc("tools/list", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, session, nil)
	if !ok || batchMax == 0 {
		return
	}
	tools, _ := result["tools"].([]any)
	got := any(nil)
	for _, tool := range tools {
		m, _ := tool.(map[string]any)
		if m["name"] == "batch" {
			schema, _ := m["inputSchema"].(map[string]any)
			props, _ := schema["properties"].(map[string]any)
			ops, _ := props["operations"].(map[string]any)
			got = ops["maxItems"]
		}
	}
	s.check("batch operations maxItems", got == float64(batchMax), "%v (%d tools)", got, len(tools))
}

func (s *smoke) rpc(name, body, session string, headers map[string]string) (map[string]any, bool) {
	started := time.Now()
	status, header, data, err := s.do("POST", s.endpoint, body, func(r *http.Request) {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		if session == "" {
			session = s.session
		}
		if session != "" {
			r.Header.Set("Mcp-Session-Id", session)
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
	})
	if issued := header.Get("Mcp-Session-Id"); name == "initialize" && issued != "" {
		s.session = issued
	}
	reply, decodeErr := decode(data)
	result, _ := reply["result"].(map[string]any)
	ok := err == nil && decodeErr == nil && status == 200 && result != nil && reply["error"] == nil && result["isError"] != true
	if name == "tools/list" {
		ok = ok && len(result["tools"].([]any)) > 0
	}
	s.check(name, ok, "status=%d session=%q %dms %v%v", status, header.Get("Mcp-Session-Id"), time.Since(started).Milliseconds(), errors.Join(err, decodeErr), errorText(reply))
	return result, ok
}

func errorText(reply map[string]any) string {
	if e, ok := reply["error"]; ok {
		return fmt.Sprintf(" error=%v", e)
	}
	return ""
}

func (s *smoke) do(method, url, body string, prepare func(*http.Request)) (int, http.Header, []byte, error) {
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if prepare != nil {
		prepare(req)
	}
	response, err := s.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	return response.StatusCode, response.Header, data, err
}

// decode reads a JSON body or the first JSON-RPC message of an SSE stream.
func decode(data []byte) (map[string]any, error) {
	var reply map[string]any
	if json.Unmarshal(data, &reply) == nil {
		return reply, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for scanner.Scan() {
		if line := scanner.Bytes(); bytes.HasPrefix(line, []byte("data: ")) && json.Unmarshal(line[6:], &reply) == nil {
			return reply, nil
		}
	}
	return nil, fmt.Errorf("no JSON-RPC message in %d bytes", len(data))
}
