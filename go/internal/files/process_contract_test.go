package files

import (
	"context"
	"testing"
	"time"
)

func TestRunCommandRetainsTextDecodingAndMinimumLimit(t *testing.T) {
	for _, tc := range []struct {
		name, script, wantStdout, wantStderr string
		limit                                int
		wantTruncated                        bool
	}{
		{
			name: "replacement decoding", limit: 4000,
			script: `import os
os.write(1, b"\xffout")
os.write(2, b"\xfeerr")
`,
			wantStdout: "\ufffdout", wantStderr: "\ufffderr",
		},
		{
			name: "zero limit permits one byte", limit: 0,
			script: "import os; os.write(1, b'x')", wantStdout: "x",
		},
		{
			name: "zero limit still caps stdout", limit: 0,
			script: "import os; os.write(1, b'xy')", wantStdout: "x", wantTruncated: true,
		},
		{
			name: "zero limit still caps stderr", limit: 0,
			script: "import os; os.write(2, b'xy')", wantStderr: "x", wantTruncated: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := RunCommand(context.Background(), t.TempDir(), "python3", []string{"-c", tc.script}, "", tc.limit, 3*time.Second)
			if result.Stdout != tc.wantStdout || result.Stderr != tc.wantStderr || result.Truncated != tc.wantTruncated || result.TimedOut {
				t.Fatalf("file-command result contract changed: %+v", result)
			}
			if tc.wantTruncated {
				if result.Err == nil || result.Err.Error() != "spawnSync python3 ENOBUFS" {
					t.Fatalf("output limit error changed: %v", result.Err)
				}
			} else if result.Err != nil || result.ExitCode != 0 {
				t.Fatalf("normal command failed: %+v", result)
			}
		})
	}
}
