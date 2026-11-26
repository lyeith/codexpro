package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProbeScopesWithUnitReturnsAttemptedUnitWithoutChangingProbe(t *testing.T) {
	for _, exitCode := range []int{0, 17} {
		t.Run(strconv.Itoa(exitCode), func(t *testing.T) {
			dir := t.TempDir()
			helper := filepath.Join(dir, "systemd-run")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$0.args\"\nexit " + strconv.Itoa(exitCode) + "\n"
			if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			unit, err := ProbeScopesWithUnit(ctx)
			assertProbeResult(t, err, exitCode)
			data, err := os.ReadFile(helper + ".args")
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			if len(args) != 8 || strings.Join(args[:5], " ") != "--user --scope --quiet --collect --unit" ||
				args[6] != "--" || args[7] != "true" || unit != args[5]+".scope" {
				t.Fatalf("probe options or exact returned unit changed: unit=%q args=%q", unit, args)
			}
			want := "^codexpro-go-probe-" + strconv.Itoa(os.Getpid()) + "-[a-f0-9]{8}\\.scope$"
			if !regexp.MustCompile(want).MatchString(unit) {
				t.Fatalf("returned scope was not this exact probe attempt: %q", unit)
			}
			// The established API preserves the same raw helper outcome.
			assertProbeResult(t, ProbeScopes(ctx), exitCode)
		})
	}
}

func assertProbeResult(t *testing.T, err error, exitCode int) {
	t.Helper()
	if exitCode == 0 {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != exitCode {
		t.Fatalf("probe result was replaced: want exit %d, got %v", exitCode, err)
	}
}
