//go:build linux

package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// This opt-in control transfers only disposable owner-created state between
// test processes and the real server. Ordinary package tests do not opt in.
func TestNamedPolicyJobStateTransfer(t *testing.T) {
	root, phase := namedPolicyJobFixture(t)
	t.Setenv("CODEXPRO_JOB_SCOPES", "0")
	cfg := config.New(filepath.Join(root, "go-state"))
	cfg.Set("codexDir", filepath.Join(root, "codex"))
	cfg.Set("jobTimeoutMs", 5000)
	cfg.Set("bashTimeoutMs", 1000)
	cfg.Set("maxBashTimeoutMs", 5000)
	resolver := testResolver{wire.Workspace{
		ID: "rehearsal-workspace", ProjectID: "fixture",
		Root: filepath.Join(root, "repo"), Mode: "workspace",
	}}
	var service *Service
	var err error
	if phase == "seed" {
		service, err = New(cfg, resolver)
	} else {
		service, err = NewPaused(cfg, resolver)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	named := wire.Identity{Source: "named", PrincipalType: "system", PrincipalID: "fleet",
		CredentialID: "fleet-connector", AgentID: "sp-fleet", AgentKind: "leaf"}
	scopes := []wire.Scope{
		{PrincipalID: "original-legacy-key", Agent: "SP3"},
		{PrincipalID: "principal:fleet", Identity: named, Agent: "SP3"},
	}
	idsPath := filepath.Join(root, "job-ids.json")
	var ids []string
	if phase == "seed" {
		if _, err := os.Lstat(idsPath); !os.IsNotExist(err) {
			t.Fatal("seed refuses an existing job fixture")
		}
		for _, scope := range scopes {
			result, err := service.Call(context.Background(), scope, "bash", map[string]any{
				"workspace_id": resolver.workspace.ID, "command": "printf fixture",
				"timeout_ms": 1000, "on_timeout": "kill",
			})
			if err != nil || result.IsError {
				t.Fatalf("fixture command failed: %v", err)
			}
			id, ok := resultData(t, result)["job_id"].(string)
			if !ok {
				t.Fatal("fixture command omitted job ID")
			}
			ids = append(ids, id)
		}
		data, err := json.Marshal(ids)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(idsPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("cannot save fixture job IDs", writeErr, closeErr)
		}
	} else {
		data, err := os.ReadFile(idsPath)
		if err != nil || json.Unmarshal(data, &ids) != nil || len(ids) != 2 {
			t.Fatal("missing original fixture job IDs")
		}
	}
	for i, id := range ids {
		record, ok := service.Get(id, resolver.workspace.ID)
		if !ok || record.Status != "succeeded" || record.ExitCode == nil || *record.ExitCode != 0 ||
			record.Agent != "SP3" || record.OwnerPrincipalID != scopes[i].PrincipalID ||
			record.Identity != scopes[i].Identity || !record.Quiescent {
			t.Fatalf("job %d lost terminal result or original owner", i)
		}
		if _, err := service.lookup(id, resolver.workspace.ID, &scopes[i].Identity); err != nil {
			t.Fatalf("original owner cannot read retained job %d: %v", i, err)
		}
	}
	foreign := named
	foreign.CredentialID = "another-device"
	for _, test := range []struct {
		id     string
		caller wire.Identity
	}{{ids[0], named}, {ids[1], wire.Identity{}}, {ids[1], foreign}} {
		if _, err := service.lookup(test.id, resolver.workspace.ID, &test.caller); err == nil {
			t.Fatal("recovery transferred a job to another identity")
		}
	}
}

func namedPolicyJobFixture(t *testing.T) (string, string) {
	t.Helper()
	root := os.Getenv("NAMED_POLICY_FIXTURE_ROOT")
	if root == "" {
		t.Skip("opt-in disposable named-policy fixture only")
	}
	phase := os.Getenv("NAMED_POLICY_FIXTURE_PHASE")
	if phase != "seed" && phase != "check" {
		t.Fatal("fixture phase must be seed or check")
	}
	real, err := filepath.EvalSymlinks(root)
	info, statErr := os.Lstat(root)
	if err != nil || statErr != nil || !filepath.IsAbs(root) || real != filepath.Clean(root) ||
		!info.IsDir() || info.Mode().Perm() != 0700 || int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		t.Fatal("fixture root must be an owned private real directory")
	}
	marker, err := os.ReadFile(filepath.Join(root, "DISPOSABLE-NAMED-POLICY-FIXTURE"))
	if err != nil || string(marker) != "named-policy-fixture-v1\n" {
		t.Fatal("missing disposable fixture marker")
	}
	return root, phase
}
