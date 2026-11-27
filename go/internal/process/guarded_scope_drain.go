package process

import (
	"context"
	"time"
)

// DrainGuardedScope must follow CloseResources for the same original claim.
// systemd owns the accepted TimeoutStopUSec clock across controller restarts.
// A repeated nonblocking stop joins the existing stop job; it changes neither
// lifetime properties nor the original deadline and never requests direct KILL.
// Removed physical owners remain unresolved here for the original helper to
// verify through FinishResources.
func DrainGuardedScope(ctx context.Context, identity *ScopeIdentity) bool {
	return drainGuardedScope(ctx, identity, InspectScope)
}

func drainGuardedScope(ctx context.Context, identity *ScopeIdentity, inspect func(context.Context, *ScopeIdentity) ScopeState) bool {
	if !ValidScopeIdentity(identity, "") || identity.CgroupDevice == 0 || identity.CgroupInode == 0 {
		return false
	}
	switch inspect(ctx, identity) {
	case ScopeQuiescent:
		return true
	case ScopeActive:
	default:
		return false
	}
	// The observer verifies boot, invocation, cgroup path, device and inode
	// immediately before requesting drainage of this exact original unit.
	if _, err := RunBounded(ctx, 2*time.Second, 4096, "systemctl", "--user", "stop", "--no-block", identity.Unit); err != nil {
		return false
	}
	return inspect(ctx, identity) == ScopeQuiescent
}
