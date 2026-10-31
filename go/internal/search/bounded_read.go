package search

import (
	"io"
	"os"
)

// Inventory metadata is advisory: a concurrent writer can grow a file before
// extraction opens it. Read at most the remaining budget plus the sentinel byte
// that drives the existing partial-coverage result.
func readAnalysisBytes(path string, remaining int) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, int64(max(0, remaining))+1))
}
