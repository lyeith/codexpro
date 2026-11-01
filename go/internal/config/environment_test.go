package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestEnvironmentFileQuotingAndContinuationGolden(t *testing.T) {
	text := strings.Join([]string{
		"# comment with ignored=value", "; another=value", " ignored line ",
		"A=  one\\ two  ", "B='single\nquoted'", "C=\"double\\$quote\\\" and\\\\slash\"",
		"D=one\\\n two", "E=\"one\\\ntwo\"", "A=last",
	}, "\n")
	got, e := parseEnvironmentFile(text)
	if e != nil {
		t.Fatal(e)
	}
	expected := map[string]string{"A": "last", "B": "single\nquoted", "C": "double$quote\" and\\slash", "D": "one two", "E": "onetwo"}
	if !reflect.DeepEqual(got.Values, expected) || !reflect.DeepEqual(got.Keys, []string{"A", "B", "C", "D", "E"}) {
		t.Fatalf("parser drift: %#v", got)
	}
	special := "X=\"x\\qy\\$z\\" + string(rune(96)) + "t\""
	got, e = parseEnvironmentFile(special)
	if e != nil {
		t.Fatal(e)
	}
	if got.Values["X"] != "x\\qy$z"+string(rune(96))+"t" {
		t.Fatal("double-quote escape semantics drifted")
	}
	for _, bad := range []string{"A='unterminated", "A=\"done\" #comment", "BAD KEY=x", "1A=x", "A=x\x00y", "A=x\ufeffy", "\xff"} {
		if _, e = parseEnvironmentFile(bad); e == nil {
			t.Fatalf("invalid EnvironmentFile accepted %q", bad)
		}
	}
}
func writeEnv(t *testing.T, c *Config, value string) {
	t.Helper()
	if e := os.WriteFile(c.EnvFile, []byte(value), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestReloadIgnoresStartupValuesAndKeepsMissingKeys(t *testing.T) {
	c := New(t.TempDir())
	before := c.Int("jobTimeoutMs", 0)
	value := strings.Repeat("DO_NOT_LOG_THIS_", 8)
	writeEnv(t, c, "CODEXPRO_MAX_JOBS=3\nCODEXPRO_HTTP_TOKEN='"+value+"'\nCODEXPRO_BASH_MODE=safe\nUNRELATED=whatever\n")
	report, e := c.ReloadOperationalLimits()
	if e != nil || !report.OK {
		t.Fatal(e)
	}
	if c.Int("maxJobs", 0) != 3 || c.Int("jobTimeoutMs", 0) != before || c.String("bashMode", "") != "full" {
		t.Fatal("reload changed startup or missing values")
	}
	if !reflect.DeepEqual(report.Ignored, []string{"CODEXPRO_HTTP_TOKEN", "CODEXPRO_BASH_MODE", "UNRELATED"}) {
		t.Fatal(report.Ignored)
	}
	if !reflect.DeepEqual(report.Changed, []string{"CODEXPRO_MAX_JOBS: 12 -> 3"}) {
		t.Fatal(report.Changed)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), value) {
		t.Fatal("report leaked source value")
	}
	writeEnv(t, c, "CODEXPRO_MAX_JOBS=4\nCODEXPRO_MAX_OUTPUT_BYTES='"+value+"'\n")
	report, e = c.ReloadOperationalLimits()
	if e == nil || report.OK || len(report.Changed) > 0 || c.Int("maxJobs", 0) != 3 {
		t.Fatal("rejected update was not atomic")
	}
	if strings.Contains(e.Error(), value) {
		t.Fatal("error leaked source value")
	}
}
func TestReloadUsesExactOperationalKeysAndIndependentTimeouts(t *testing.T) {
	c := New(t.TempDir())
	writeEnv(t, c, "maxJobs=1\nCODEXPRO_MAX_READ_BYTES=9999\nCODEXPRO_BASH_TIMEOUT_MS=900000\nCODEXPRO_MAX_BASH_TIMEOUT_MS=1000\nCODEXPRO_MAX_JOBS_PER_WORKSPACE=32\nCODEXPRO_MAX_JOBS=1\n")
	report, e := c.ReloadOperationalLimits()
	if e != nil || !report.OK {
		t.Fatal("baseline permits independently bounded timeout and capacity settings", e)
	}
	if c.Int("maxReadBytes", 0) != 180000 || c.Int("maxJobs", 0) != 1 || c.Int("maxJobsPerWorkspace", 0) != 32 || c.Int("bashTimeoutMs", 0) != 900000 || c.Int("maxBashTimeoutMs", 0) != 1000 {
		t.Fatal("operational key set or independent bounds drifted")
	}
	if !reflect.DeepEqual(report.Ignored, []string{"maxJobs", "CODEXPRO_MAX_READ_BYTES"}) {
		t.Fatal(report.Ignored)
	}
}
func TestReloadBoundariesAndJavaScriptNumbers(t *testing.T) {
	c := New(t.TempDir())
	for _, spec := range operationalLimits {
		for _, value := range []int{spec.Min, spec.Max} {
			writeEnv(t, c, fmt.Sprintf("%s=%d\n", spec.Key, value))
			if e := c.Reload(); e != nil || c.Int(spec.Field, 0) != value {
				t.Fatalf("%s boundary %d: %v", spec.Key, value, e)
			}
		}
		before := c.Int(spec.Field, 0)
		for _, value := range []string{"", fmt.Sprint(spec.Min - 1), fmt.Sprint(spec.Max + 1), "1.5", "NaN", "Infinity", "9007199254740993"} {
			writeEnv(t, c, spec.Key+"="+value+"\n")
			if e := c.Reload(); e == nil || c.Int(spec.Field, 0) != before {
				t.Fatalf("invalid %s was accepted or partially applied", spec.Key)
			}
		}
	}
	for text, want := range map[string]int{"0x10": 16, "0b101": 5, "0o10": 8, "2e1": 20, "+3": 3, " 4 ": 4} {
		writeEnv(t, c, "CODEXPRO_MAX_JOBS='"+text+"'\n")
		if e := c.Reload(); e != nil || c.Int("maxJobs", 0) != want {
			t.Fatalf("JavaScript numeric form %q: %v", text, e)
		}
	}
}
func TestEnvironmentBoundsAndInvalidEncoding(t *testing.T) {
	c := New(t.TempDir())
	writeEnv(t, c, strings.Repeat(" ", 1024*1024+1))
	if e := c.Reload(); e == nil || e.Error() != "EnvironmentFile exceeds 1 MiB" {
		t.Fatal(e)
	}
	for _, value := range []string{"\xef\xbb\xbfCODEXPRO_MAX_JOBS=3\n", "CODEXPRO_MAX_JOBS=3\x00\n", "\xff"} {
		writeEnv(t, c, value)
		if e := c.Reload(); e == nil {
			t.Fatal("invalid encoding accepted")
		}
	}
	if e := os.Remove(c.EnvFile); e != nil {
		t.Fatal(e)
	}
	if e := c.Reload(); !os.IsNotExist(e) {
		t.Fatalf("missing private file: %v", e)
	}
	c.EnvFile = filepath.Join(t.TempDir(), "directory")
	if e := os.Mkdir(c.EnvFile, 0700); e != nil {
		t.Fatal(e)
	}
	if e := c.Reload(); e == nil {
		t.Fatal("non-regular environment source accepted")
	}
}
func TestConcurrentReloadSnapshotsRemainAtomic(t *testing.T) {
	c := New(t.TempDir())
	c.Set("maxJobs", 3)
	c.Set("maxJobsPerWorkspace", 3)
	var wg sync.WaitGroup
	done := make(chan struct{})
	problem := make(chan string, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				state := c.Snapshot()
				if state["maxJobs"] != state["maxJobsPerWorkspace"] {
					select {
					case problem <- "partial operational update observed":
					default:
					}
					return
				}
			}
		}
	}()
	for i := 0; i < 100; i++ {
		n := 3 + i%2
		writeEnv(t, c, fmt.Sprintf("CODEXPRO_MAX_JOBS=%d\nCODEXPRO_MAX_JOBS_PER_WORKSPACE=%d\n", n, n))
		c.Set("startupSentinel", i)
		if e := c.Reload(); e != nil {
			t.Fatal(e)
		}
	}
	close(done)
	wg.Wait()
	select {
	case message := <-problem:
		t.Fatal(message)
	default:
	}
	if c.Int("startupSentinel", -1) != 99 {
		t.Fatal("reload overwrote unrelated startup state")
	}
}
func TestStartupNumericFallbackAndClamping(t *testing.T) {
	for _, tc := range []struct {
		value   string
		present bool
		want    int
	}{{"", false, 12}, {"bad", true, 12}, {"", true, 1}, {"65.9", true, 64}, {"-3", true, 1}, {"0x10", true, 16}, {"+0x10", true, 12}} {
		if got := startupNumber(tc.value, tc.present, 12, 1, 64); got != tc.want {
			t.Fatalf("%q: got %d want %d", tc.value, got, tc.want)
		}
	}
	if New(t.TempDir()).Int("jobTimeoutMs", 0) != 2700000 {
		t.Fatal("source fallback must be 45 minutes")
	}
}

func TestDrainTimeoutFollowsTypeScriptBounds(t *testing.T) {
	for _, tc := range []struct {
		value   string
		present bool
		want    int
	}{{"", false, 30000}, {"60000", true, 60000}, {"0", true, 0}, {"", true, 0}, {"-5", true, 30000}, {"soon", true, 30000}, {"1500.9", true, 1500}, {"900000", true, 600000}} {
		if got := drainTimeoutMs(tc.value, tc.present); got != tc.want {
			t.Fatalf("drainTimeoutMs(%q, %v) = %d, want %d", tc.value, tc.present, got, tc.want)
		}
	}
}
