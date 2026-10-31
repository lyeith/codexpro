package files

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Import outputs retain the ordinary create mode (0666 filtered by the process
// umask), as the Node createWriteStream implementation does. os.CreateTemp would
// silently change every imported file to 0600.
func createImportTemp() (*os.File, error) {
	raw := make([]byte, 8)
	if _, e := rand.Read(raw); e != nil {
		return nil, e
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("codexpro-import-%d-%s.bin", os.Getpid(), hex.EncodeToString(raw)))
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
}
