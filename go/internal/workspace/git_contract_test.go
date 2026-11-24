package workspace

import (
	"context"
	"testing"
)

func TestGitOutputAndErrorContracts(t *testing.T) {
	for _, tc := range []struct {
		name, script, wantOutput, wantError string
		limit                               int
	}{
		{
			name: "raw success", limit: 4000,
			script: `import os
assert os.environ["NO_COLOR"] == "1"
assert os.environ["GIT_TERMINAL_PROMPT"] == "0"
os.write(1, b" \xffout\n")
`,
			wantOutput: "\xffout",
		},
		{
			name: "stderr precedes stdout and exit", limit: 4000,
			script: `import os, sys
os.write(1, b" \xffout\n")
os.write(2, b" \xfeerr\n")
sys.exit(7)
`,
			wantError: "\xfeerr",
		},
		{
			name: "stdout precedes exit", limit: 4000,
			script: `import os, sys
os.write(1, b" \xffout\n")
sys.exit(7)
`,
			wantError: "\xffout",
		},
		{
			name: "exit fallback", limit: 4000,
			script: "import sys; sys.exit(7)", wantError: "exit status 7",
		},
		{
			name: "zero limit permits empty output", limit: 0,
			script: "pass",
		},
		{
			name: "zero stdout limit", limit: 0,
			script: "import os; os.write(1, b'x')", wantError: "Git output exceeded 0 bytes.",
		},
		{
			name: "zero stderr limit", limit: 0,
			script: "import os; os.write(2, b'x')", wantError: "Git output exceeded 0 bytes.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := fixture(t)
			m.cfg.Set("maxOutputBytes", tc.limit)
			fakeGit(t, tc.script)
			output, err := m.runGit(context.Background(), t.TempDir(), "contract")
			if tc.wantError == "" {
				if err != nil || output != tc.wantOutput {
					t.Fatalf("output=%q error=%v, want %q", output, err, tc.wantOutput)
				}
			} else if err == nil || err.Error() != tc.wantError || output != "" {
				t.Fatalf("output=%q error=%v, want %q", output, err, tc.wantError)
			}
		})
	}
}
