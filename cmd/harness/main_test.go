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
func TestUnimplementedCommandsFailHonestly(t *testing.T) {
	for _, name := range []string{"demo", "tui", "worker", "run", "doctor", "session", "approve", "deny", "wait"} {
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
