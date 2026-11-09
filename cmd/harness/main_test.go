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
	if execute([]string{"unknown"}, &bytes.Buffer{}) == nil {
		t.Fatal("unknown command accepted")
	}
}
func TestUnimplementedCommandsFailHonestly(t *testing.T) {
	for _, name := range []string{"demo", "tui", "worker", "run", "doctor", "session", "approve", "deny", "wait"} {
		if execute([]string{name}, &bytes.Buffer{}) == nil {
			t.Errorf("%s silently succeeded", name)
		}
	}
}
