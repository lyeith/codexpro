// CodexPro Go runs only as an explicitly launched, isolated HTTP process.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lyeith/codexpro/go/internal/activity"
	"github.com/lyeith/codexpro/go/internal/admin"
	"github.com/lyeith/codexpro/go/internal/batch"
	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/engine"
	"github.com/lyeith/codexpro/go/internal/inbox"
	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/transport"
	"github.com/lyeith/codexpro/go/internal/usage"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workhistory"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

func main() {
	var err error
	// Detached runners consume a private, immutable launch spec and never load
	// server configuration, start listeners, or create an unrelated bearer token.
	if len(os.Args) > 1 && os.Args[1] == "runner" {
		err = process.RunRunner(os.Args[2:])
	} else {
		err = run(os.Args[1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "CodexPro Go:", wire.Redact(err.Error()))
		os.Exit(1)
	}
}
func run(args []string) error {
	lifetime, stopLifetime := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopLifetime()
	reloads := make(chan os.Signal, 1)
	signal.Notify(reloads, syscall.SIGHUP)
	defer signal.Stop(reloads)
	cfg, err := config.Load(args)
	if err != nil {
		return err
	}
	manager, err := workspace.New(cfg)
	if err != nil {
		return err
	}
	managerOwned := true
	defer func() {
		if managerOwned {
			_ = manager.Close()
		}
	}()
	health, err := admin.NewHealthHandler(cfg, manager, adminHTTPOptions(engine.Redactor{Config: cfg, Workspaces: manager}))
	if err != nil {
		return err
	}
	bootstrap, err := transport.NewBootstrap(cfg, health)
	if err != nil {
		return err
	}
	defer bootstrap.Close()
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	fence := &readyListener{Listener: listener, ready: make(chan struct{})}
	httpServer := &http.Server{Handler: bootstrap, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 64 * 1024}
	defer httpServer.Close()
	ended := make(chan error, 1)
	go func() { ended <- httpServer.Serve(fence) }()
	stopStartup := context.AfterFunc(lifetime, func() { _ = bootstrap.Close(); _ = httpServer.Close() })
	defer stopStartup()
	select {
	case <-fence.ready:
	case err := <-ended:
		if lifetime.Err() != nil && errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-lifetime.Done():
		return nil
	}
	if lifetime.Err() != nil {
		return nil
	}
	// Health is authenticated and serving before audit replay, job recovery,
	// host capability probes or optional historical-store initialization.
	managerOwned = false
	e, err := engine.NewPausedWithWorkspaces(cfg, manager)
	if err != nil {
		return err
	}
	defer e.Close()
	principal, err := e.PrincipalID()
	if err != nil {
		return err
	}
	administrative, err := admin.New(cfg, e.Workspaces, e.Files, e.Git, e.Registry, e.Audit)
	if err != nil {
		return err
	}
	e.Admin = administrative
	administrative.RegisteredNames = e.RegisteredNames
	batches, err := batch.New(cfg, e.Workspaces, e, func(name string, args map[string]any) (map[string]any, error) {
		if _, ok := e.Registry.Tool(name); !ok {
			return nil, wire.Errorf("batch_child_not_allowed", "Tool %s is not available in the current CodexPro mode.", name)
		}
		validated, err := e.Registry.Validate(name, args)
		if typed, ok := err.(*contracts.ValidationError); ok {
			return nil, typed.BusinessError()
		}
		return validated, err
	})
	if err != nil {
		return err
	}
	batches.SetFiles(e.Files)
	batches.SetJobLookup(func(id, workspace string) (bool, bool) {
		record, ok := e.Jobs.Get(id, workspace)
		return ok, record.Quiescent
	})
	e.Batch = batches
	historyPath := cfg.String("workHistory", "")
	mapping := map[string]string{}
	if historyPath != "" {
		mapPath := filepath.Join(filepath.Dir(historyPath), "principal-map.json")
		data, readErr := os.ReadFile(mapPath)
		if readErr == nil {
			if err = json.Unmarshal(data, &mapping); err != nil {
				return fmt.Errorf("invalid explicit archived-principal map: %w", err)
			}
		} else if !os.IsNotExist(readErr) {
			return readErr
		}
	}
	history := workhistory.NewDeferred(historyPath, workhistory.Options{Audit: e.Audit, PacketBytes: cfg.Int("workPacketBytes", 16384), ContextDir: cfg.String("contextDir", ".ai-bridge"), PrincipalMap: mapping})
	defer history.Close()
	e.WorkHistory = history
	server, err := transport.New(cfg, e.Registry, e, principal, e.DropSession)
	if err != nil {
		return err
	}
	defer server.Close()
	administrative.RegisterRoutes(server.Mux, adminHTTPOptions(e.Redactor))
	projectExists := func(id string) bool { _, ok := e.Workspaces.Project(id); return ok }
	usageHTTP := usage.RegisterRoutes(server.Mux, usage.HTTPOptions{Directory: filepath.Join(cfg.Home, "usage"), ProjectExists: projectExists, Principal: server.Principal, SameOrigin: transport.SameOrigin, Projects: e.Workspaces.Projects})
	defer usageHTTP.Close()
	inboxHTTP := inbox.RegisterRoutes(server.Mux, inbox.HTTPOptions{Directory: filepath.Join(cfg.Home, "inbox"), ProjectExists: projectExists, Principal: server.Principal, SameOrigin: transport.SameOrigin, Projects: e.Workspaces.Projects})
	defer inboxHTTP.Close()
	activityHTTP := activity.RegisterRoutes(server.Mux, activity.Options{Config: cfg, Resolver: e.Workspaces, Audit: e.Audit, Jobs: e.Jobs,
		Git: e.Git.SnapshotGit, SafePath: e.Git.SafeDashboardPath,
		Work: &activity.WorkHistory{Runs: history.Runs, Iteration: history.Iteration, OperationCount: history.OperationCount},
		Batch: func(ctx context.Context, projectID, workspaceID, path string) (map[string]any, error) {
			project, ok := e.Workspaces.Project(projectID)
			if !ok {
				return nil, fmt.Errorf("unknown project")
			}
			ws, err := activityBatchWorkspace(ctx, e.Workspaces, principal, project, workspaceID)
			if err != nil {
				return nil, err
			}
			view, err := batches.ReadDefinition(ctx, ws, path)
			if err != nil {
				return nil, err
			}
			view["projectId"] = project.ID
			view["projectLabel"] = project.Label
			view["workspaceId"] = ws.ID
			return view, nil
		},
	})
	defer activityHTTP.Close()
	// Drain HTTP before domain stores on every post-publication exit, including
	// a signal that arrives between publication and the serving select loop.
	defer func() {
		server.BeginDrain()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			_ = httpServer.Close()
		}
	}()
	if lifetime.Err() != nil || !stopStartup() {
		return nil
	}
	if lifetime.Err() != nil {
		return nil
	}
	if err = bootstrap.Publish(server); err != nil {
		return err
	}
	if lifetime.Err() != nil {
		return nil
	}
	e.StartBackground()
	history.Start(lifetime)
	// This is an operator log. It contains no credential or authenticated URL.
	fmt.Fprintf(os.Stderr, "CodexPro Go listening on %s (pid=%d, private state=%s)\n", listener.Addr(), os.Getpid(), cfg.Home)
	for {
		select {
		case err := <-ended:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-reloads:
			if err := cfg.Reload(); err != nil {
				fmt.Fprintln(os.Stderr, "Private limit reload rejected:", wire.Redact(err.Error()))
			} else {
				e.Audit.SetLimits(cfg.Int64("auditMaxBytes", 8388608), cfg.Int("auditRetainActions", 350), cfg.Int("maxOutputBytes", 120000))
			}
		case <-lifetime.Done():
			server.BeginDrain()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err := httpServer.Shutdown(ctx)
			cancel()
			if err != nil {
				_ = httpServer.Close()
			}
			<-ended
			return nil
		}
	}
}
