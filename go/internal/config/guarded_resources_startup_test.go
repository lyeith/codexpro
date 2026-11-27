package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardedResourcesDefaultDisabledAndProfileIgnored(t *testing.T) {
	state, root := startupFixture(t)
	writeStartupProfile(t, state, root, map[string]any{
		"guardedHostResources": true, "guardedHostResourcesHelper": "/profile/helper",
		"hostResourcesConfig": "/profile/config", "hostResourcesStopTimeoutMs": 1,
	})
	c, err := Load(startupArgs(state))
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []*Config{New(state), c} {
		if current.Bool("guardedHostResources", true) || current.String("guardedHostResourcesHelper", "missing") != "" || current.String("hostResourcesConfig", "missing") != "" || current.Int("hostResourcesStopTimeoutMs", 0) != 90000 {
			t.Fatal("guarded defaults or startup-only profile boundary drifted")
		}
	}
}

func TestGuardedResourcesStartupPrecedenceKeepsLegacyHelper(t *testing.T) {
	state, _ := startupFixture(t)
	legacyFile := filepath.Join(state, "legacy-file")
	helperFile := filepath.Join(state, "guarded-file")
	configFile := filepath.Join(state, "producer-file")
	body := "CODEXPRO_GUARDED_HOST_RESOURCES=true\nCODEXPRO_GUARDED_HOST_RESOURCES_HELPER=" + helperFile +
		"\nCODEXPRO_HOST_RESOURCES_CONFIG=" + configFile + "\nCODEXPRO_HOST_RESOURCES_STOP_TIMEOUT_MS=12000\nCODEXPRO_HOST_RESOURCES_HELPER=" + legacyFile + "\n"
	if err := os.WriteFile(filepath.Join(state, "limits.env"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	check := func(args []string, legacy, helper, producer string, stop int) {
		t.Helper()
		c, err := Load(args)
		if err != nil {
			t.Fatal(err)
		}
		if !c.Bool("guardedHostResources", false) || c.HostResourcesHelper != legacy || c.String("guardedHostResourcesHelper", "") != helper || c.String("hostResourcesConfig", "") != producer || c.Int("hostResourcesStopTimeoutMs", 0) != stop {
			t.Fatal("guarded startup precedence or independent legacy helper selection drifted")
		}
	}
	check(startupArgs(state), legacyFile, helperFile, configFile, 12000)
	legacyEnv := filepath.Join(state, "legacy-environment")
	helperEnv := filepath.Join(state, "guarded-environment")
	configEnv := filepath.Join(state, "producer-environment")
	t.Setenv("CODEXPRO_HOST_RESOURCES_HELPER", legacyEnv)
	t.Setenv("CODEXPRO_GUARDED_HOST_RESOURCES_HELPER", helperEnv)
	t.Setenv("CODEXPRO_HOST_RESOURCES_CONFIG", configEnv)
	t.Setenv("CODEXPRO_HOST_RESOURCES_STOP_TIMEOUT_MS", "34000")
	check(startupArgs(state), legacyEnv, helperEnv, configEnv, 34000)

	t.Setenv("CODEXPRO_GUARDED_HOST_RESOURCES", "false")
	c, err := Load(startupArgs(state))
	if err != nil || c.Bool("guardedHostResources", true) {
		t.Fatal("process environment did not disable file opt-in", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	legacyCLI := filepath.Join(state, "legacy-cli")
	check(append(startupArgs(state), "--guarded-host-resources", "--host-resources-helper", legacyCLI,
		"--guarded-host-resources-helper", "./guarded-candidate/helper", "--host-resources-config", "./guarded-candidate/policy.json",
		"--host-resources-stop-timeout-ms", "56000"),
		legacyCLI, filepath.Join(cwd, "guarded-candidate", "helper"), filepath.Join(cwd, "guarded-candidate", "policy.json"), 56000)

	t.Setenv("CODEXPRO_GUARDED_HOST_RESOURCES", "true")
	c, err = Load(append(startupArgs(state), "--guarded-host-resources=false", "--guarded-host-resources-helper=", "--host-resources-config="))
	if err != nil || c.Bool("guardedHostResources", true) || c.String("guardedHostResourcesHelper", "missing") != "" || c.String("hostResourcesConfig", "missing") != "" || c.HostResourcesHelper != legacyEnv {
		t.Fatal("explicit disabled selection changed the legacy helper or inherited opt-in", err)
	}
}

func TestGuardedResourcesStartupRequiresIndependentSelection(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"neither", nil, "requires guarded-host-resources-helper and host-resources-config"},
		{"helper-only", []string{"--guarded-host-resources-helper", "/private/guarded-helper"}, "requires"},
		{"config-only", []string{"--host-resources-config", "/private/policy.json"}, "requires"},
		{"legacy-is-not-guarded", []string{"--host-resources-helper", "/private/legacy-helper", "--host-resources-config", "/private/policy.json"}, "requires"},
		{"blank-helper", []string{"--guarded-host-resources-helper", "  ", "--host-resources-config", "/private/policy.json"}, "path without control characters"},
		{"control-helper", []string{"--guarded-host-resources-helper", "/private/helper\x00hidden", "--host-resources-config", "/private/policy.json"}, "path without control characters"},
		{"control-config", []string{"--guarded-host-resources-helper", "/private/helper", "--host-resources-config", "/private/policy\nhidden"}, "path without control characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, _ := startupFixture(t)
			args := append(startupArgs(state), "--guarded-host-resources")
			if _, err := Load(append(args, tc.args...)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("incomplete or malformed guarded selection accepted: %v", err)
			}
		})
	}
	state, _ := startupFixture(t)
	c, err := Load(append(startupArgs(state), "--guarded-host-resources-helper", "/private/unselected-helper"))
	if err != nil || c.Bool("guardedHostResources", true) {
		t.Fatal("an incomplete but disabled selection should not block startup", err)
	}
}

func TestGuardedResourcesStateValidationWaitsForAcceptance(t *testing.T) {
	state, _ := startupFixture(t)
	helper := filepath.Join(state, "unexecuted-helper")
	producer := filepath.Join(state, "unread-config")
	c, err := Load(append(startupArgs(state), "--guarded-host-resources", "--guarded-host-resources-helper", helper, "--host-resources-config", producer))
	if err != nil {
		t.Fatal("custom-state startup must retain read and legacy access", err)
	}
	if c.Home != state || !c.Bool("guardedHostResources", false) {
		t.Fatal("startup rewrote the selected instance state")
	}
	for _, path := range []string{helper, producer} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("startup created or accessed a missing producer input")
		}
	}
}

func TestGuardedResourcesStopTimeoutBounds(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{
		{"1", 1}, {"90000", 90000}, {"600000", 600000},
		{"0", 1}, {"-1", 1}, {"600001", 600000}, {"invalid", 90000},
	} {
		t.Run(tc.value, func(t *testing.T) {
			state, _ := startupFixture(t)
			c, err := Load(append(startupArgs(state), "--guarded-host-resources", "--guarded-host-resources-helper", "/private/helper",
				"--host-resources-config", "/private/policy.json", "--host-resources-stop-timeout-ms", tc.value))
			if err != nil || c.Int("hostResourcesStopTimeoutMs", 0) != tc.want {
				t.Fatalf("effective stop allowance differs from the startup bound: %v", err)
			}
		})
	}
}

func TestGuardedResourcesReloadCannotChangeSelection(t *testing.T) {
	state, _ := startupFixture(t)
	legacy := filepath.Join(state, "legacy-helper")
	helper := filepath.Join(state, "guarded-helper")
	producer := filepath.Join(state, "producer-config")
	c, err := Load(append(startupArgs(state), "--guarded-host-resources", "--host-resources-helper", legacy,
		"--guarded-host-resources-helper", helper, "--host-resources-config", producer, "--host-resources-stop-timeout-ms", "12345"))
	if err != nil {
		t.Fatal(err)
	}
	body := "CODEXPRO_GUARDED_HOST_RESOURCES=false\nCODEXPRO_GUARDED_HOST_RESOURCES_HELPER=/later/helper\nCODEXPRO_HOST_RESOURCES_CONFIG=/later/config\nCODEXPRO_HOST_RESOURCES_STOP_TIMEOUT_MS=1\nCODEXPRO_HOST_RESOURCES_HELPER=/later/legacy\n"
	if err := os.WriteFile(c.EnvFile, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := c.ReloadOperationalLimits()
	if err != nil || !report.OK || len(report.Changed) != 0 || len(report.Ignored) != 5 {
		t.Fatal("guarded startup keys were not ignored on reload", err)
	}
	if !c.Bool("guardedHostResources", false) || c.HostResourcesHelper != legacy || c.String("guardedHostResourcesHelper", "") != helper || c.String("hostResourcesConfig", "") != producer || c.Int("hostResourcesStopTimeoutMs", 0) != 12345 {
		t.Fatal("operational reload changed a guarded or legacy startup selection")
	}
}
