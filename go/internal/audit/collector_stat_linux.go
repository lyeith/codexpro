package audit

import (
	"os"
	"syscall"
)

const collectorCacheSupported = true

// Change time also invalidates same-size writes whose mtime was restored.
func collectorChangeTime(info os.FileInfo) int64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Ctim.Nano()
	}
	return 0
}
