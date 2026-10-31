package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type replaySearchErrorResolver struct{ workspace wire.Workspace }

func (r replaySearchErrorResolver) Resolve(context.Context, wire.Scope, string) (wire.Workspace, error) {
	return r.workspace, nil
}
func (r replaySearchErrorResolver) Projects() []wire.Project {
	return []wire.Project{{ID: r.workspace.ProjectID, Root: r.workspace.Root}}
}
func (r replaySearchErrorResolver) Project(id string) (wire.Project, bool) {
	return wire.Project{ID: r.workspace.ProjectID, Root: r.workspace.Root}, id == r.workspace.ProjectID
}

func TestReplaySearchDomainErrorsMatchTypeScriptClass(t *testing.T) {
	root := t.TempDir()
	cfg := config.New(t.TempDir())
	cfg.AllowedRoots = []string{root}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	resolver := replaySearchErrorResolver{wire.Workspace{ID: "ws_error_contract", ProjectID: "fixture", Root: root, Mode: "direct"}}
	fileService, err := files.New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(cfg, resolver, fileService)
	if err != nil {
		t.Fatal(err)
	}
	scope := wire.Scope{PrincipalID: "error-contract", SessionID: "session", RequestID: "1", TransportID: "http"}
	for _, test := range []struct {
		name   string
		args   map[string]any
		prefix string
	}{
		{"invalid-rg", map[string]any{"query": "[", "regex": true}, "rg: regex parse error:"},
		{"regex-config", map[string]any{"query": "x", "regex": true, "kind": "config"}, "regex is not used by configuration queries. Use wildcards in the query path instead."},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := map[string]any{"workspace_id": "ws_error_contract"}
			for key, value := range test.args {
				args[key] = value
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := service.Call(ctx, scope, "search", args)
			var domain *wire.Error
			if err == nil || !errors.As(err, &domain) {
				t.Fatalf("source search CodexProError was returned as generic error: %T %v", err, err)
			}
			if domain.Code != "" || !strings.HasPrefix(domain.Message, test.prefix) {
				t.Fatalf("search error contract changed: %+v", domain)
			}
			result := wire.ErrorResult(err)
			expected := "CodexProError: " + domain.Message
			if !result.IsError || len(result.Content) != 1 || len(result.StructuredContent) != 1 || result.Content[0]["text"] != expected || result.StructuredContent["error"] != expected {
				t.Fatalf("search error lost class: %+v", result)
			}
		})
	}
}
