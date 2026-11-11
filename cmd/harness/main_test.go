package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpListsCommands(t *testing.T) {
	var out bytes.Buffer
	if err := execute([]string{"--help"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"demo", "tui", "worker", "run", "doctor", "session", "approve", "deny", "wait"} {
		if !strings.Contains(out.String(), name) {
			t.Errorf("help missing %s", name)
		}
	}
}
func TestUnknownCommandFails(t *testing.T) {
	err := execute([]string{"not-a-command"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("unknown command accepted")
	}
	for _, fragment := range []string{"unknown command", "not-a-command"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q missing %q", err, fragment)
		}
	}
	if strings.Contains(err.Error(), "not implemented") {
		t.Errorf("unknown command treated as scaffold command: %v", err)
	}
}
func TestDoctorFlags(t *testing.T) {
	var out bytes.Buffer
	if err := execute([]string{"doctor", "--help"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "fix") {
		t.Fatal("missing fix flag")
	}
	err := execute([]string{"doctor", "--unknown"}, &out)
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("unknown flag: %v", err)
	}
	if err := execute([]string{"doctor", "unexpected"}, &out); err == nil {
		t.Fatal("positional argument accepted")
	}
}

func TestUnimplementedCommandsFailHonestly(t *testing.T) {
	for _, name := range []string{"demo", "tui", "worker", "run", "session", "approve", "deny", "wait"} {
		t.Run(name, func(t *testing.T) {
			err := execute([]string{name}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("command silently succeeded")
			}
			for _, fragment := range []string{name + ":", "not implemented", "scaffold"} {
				if !strings.Contains(err.Error(), fragment) {
					t.Errorf("error %q missing %q", err, fragment)
				}
			}
			if strings.Contains(err.Error(), "unknown command") {
				t.Errorf("recognized command rejected as unknown: %v", err)
			}
		})
	}
}
