//go:build !linux

package audit

import "os"

// Preserve full validation on hosts without the native change-time guard.
const collectorCacheSupported = false

func collectorChangeTime(info os.FileInfo) int64 { return 0 }
