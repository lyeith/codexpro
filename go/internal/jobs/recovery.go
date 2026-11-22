package jobs

import (
	"context"

	"github.com/lyeith/codexpro/go/internal/process"
)

func settleResources(ctx context.Context, claim process.ResourceClaim, commandStarted bool, exitCode int) error {
	if commandStarted {
		if _, err := process.FinishResources(ctx, claim, exitCode); err == nil {
			return nil
		}
	}
	// Without evidence that the command started, a begin call may still
	// reach the helper later. Cancel revokes the persisted registration.
	_, err := process.CancelResources(ctx, claim)
	return err
}
