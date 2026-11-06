package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPeopleCommandPathIsTrustedStartupOnly(t *testing.T) {
	state, root := startupFixture(t)
	profileCommand := filepath.Join(state, "profile-command")
	writeStartupProfile(t, state, root, map[string]any{"plansAccessCommand": profileCommand})
	c, err := Load(startupArgs(state))
	if err != nil {
		t.Fatal(err)
	}
	if c.String("plansAccessCommand", "") != "" {
		t.Fatal("saved profile selected the People executable")
	}
	envCommand := filepath.Join(state, "environment-command")
	t.Setenv("CODEXPRO_PLANS_ACCESS_COMMAND", envCommand)
	c, err = Load(startupArgs(state))
	if err != nil || c.String("plansAccessCommand", "") != envCommand {
		t.Fatal("trusted startup environment was not applied", err)
	}
	cliCommand := filepath.Join(state, "cli-command")
	c, err = Load(append(startupArgs(state), "--plans-access-command", cliCommand))
	if err != nil || c.String("plansAccessCommand", "") != cliCommand {
		t.Fatal("explicit startup command did not override environment", err)
	}
	if err := os.WriteFile(filepath.Join(state, "limits.env"), []byte("CODEXPRO_PLANS_ACCESS_COMMAND="+profileCommand+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	if c.String("plansAccessCommand", "") != cliCommand {
		t.Fatal("operational reload changed the People executable")
	}
}

func TestPeopleCommandPreservesTypeScriptStateIsolation(t *testing.T) {
	state, _ := startupFixture(t)
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(userHome, ".codexpro", "people-owner-must-not-be-opened")
	if _, err := Load(append(startupArgs(state), "--plans-access-command", command)); err == nil {
		t.Fatal("People executable allowed a path in retained TypeScript state")
	}
}
