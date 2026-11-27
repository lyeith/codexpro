package process

import (
	"errors"
	"math"
	"time"
)

const legacyStopEscalation = 1500 * time.Millisecond

// GuardedStopAllowance is the original accepted upper bound for ordinary
// requested stop and timeout drainage. It starts after Close permits TERM;
// an earlier request timestamp is not evidence that TERM was delivered.
func GuardedStopAllowance(claim ResourceClaim) (time.Duration, error) {
	if claim.Version != 2 || claim.StopTimeoutUSec == 0 || claim.StopTimeoutUSec > uint64(math.MaxInt64)/uint64(time.Microsecond) {
		return 0, errors.New("guarded stop allowance is not a finite representable duration")
	}
	return time.Duration(claim.StopTimeoutUSec) * time.Microsecond, nil
}

func runnerStopAllowance(spec Spec, reason string) (time.Duration, error) {
	if spec.Version != GuardedRunnerSpecVersion || reason == "lost" {
		// Internal capture failures may drain promptly. They do not redefine
		// the accepted allowance for ordinary requested stops or expiration.
		return legacyStopEscalation, nil
	}
	if spec.ResourceClaim == nil {
		return 0, errors.New("guarded stop lost its accepted original claim")
	}
	return GuardedStopAllowance(*spec.ResourceClaim)
}
