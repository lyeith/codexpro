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
	// A delayed begin may still reach the helper. Cancel durably revokes
	// the original registration or confirms its already settled outcome.
	_, err := process.CancelResources(ctx, claim)
	return err
}
