package admin

import (
	"runtime/debug"
	"sync"
)

// buildRevision is set at release build time:
//
//	go build -ldflags "-X github.com/lyeith/codexpro/go/internal/admin.buildRevision=$(git rev-parse HEAD)"
//
// Go does not stamp VCS data in a linked Git worktree, where .git is a file.
var buildRevision string

// Revision is the source commit of this binary (12 hex digits, with "-dirty"
// for a modified checkout), or "unknown" when the build carries no stamp.
var Revision = sync.OnceValue(func() string {
	if buildRevision != "" {
		return buildRevision[:min(12, len(buildRevision))]
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	revision, modified := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return "unknown"
	}
	revision = revision[:min(12, len(revision))]
	if modified {
		revision += "-dirty"
	}
	return revision
})
