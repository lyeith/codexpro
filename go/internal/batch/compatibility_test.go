package batch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestFullModeCommandFailureRemainsAnExecutionFailure(t *testing.T) {
	var service *Service
	created, _, workspace := fixture(t, func(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
		if name == "bash" {
			return wire.Result{}, wire.Errorf("args_invalid", "command must be valid text without NUL bytes.")
		}
		return service.fileService().Call(ctx, scope, name, args)
	})
	service = created
	result := invoke(t, service, map[string]any{"persist": false, "operations": []any{
		op("write", "write", map[string]any{"path": "committed.txt", "content": "committed before failed verification\n"}),
		op("verify", "bash", map[string]any{"command": "printf ok\x00"}),
	}})
	if !result.IsError || result.StructuredContent["succeeded_count"] != 1 || result.StructuredContent["failed_operation_id"] != "verify" {
		t.Fatalf("full-mode execution failure was incorrectly rejected by preflight: %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(workspace.Root, "committed.txt"))
	if err != nil || string(content) != "committed before failed verification\n" {
		t.Fatalf("earlier mutation was not executed: %q %v", content, err)
	}
}
