package devenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
)

func miseTools(t *testing.T, temporal, ollama string) {
	t.Helper()
	bin, tools := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{"temporal": temporal, "ollama": ollama} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\n"+body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "mise"), []byte("#!/bin/sh\n[ \"$#\" = 2 ] && [ \"$1\" = which ] || exit 9\nprintf '%s/%s\\n' '"+tools+"' \"$2\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func TestMiseTemporalHealthRegistrationAndStart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	calls := filepath.Join(t.TempDir(), "calls")
	miseTools(t, "echo \"$*\" >> '"+calls+"'\nif [ \"$1\" = server ]; then\n : > '"+marker+"'\n exec /bin/sleep 30\nfi\n[ -f '"+marker+"' ]\n", "exit 1\n")
	m := New(config.Config{Model: "mock", TemporalAddress: "127.0.0.1:0", DataDir: t.TempDir()})
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Check(ctx, true, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.owned) != 1 {
		t.Fatalf("owned %d processes", len(m.owned))
	}
	other := New(m.cfg)
	defer other.Close()
	if err := other.Check(ctx, false, nil); err != nil {
		t.Fatal(err)
	}
	if len(other.owned) != 0 {
		t.Fatal("adopted existing service")
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"operator cluster health", "operator search-attribute create", "server start-dev"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q: %s", want, data)
		}
	}
}

func TestMiseOllamaStart(t *testing.T) {
	miseTools(t, "exit 0\n", "[ \"$1\" = serve ] || exit 9\nexec /bin/sleep 30\n")
	m := New(config.Config{Model: "test", OllamaHost: "127.0.0.1:0", DataDir: t.TempDir()})
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Check(ctx, true, nil); err == nil {
		t.Fatal("fake service should not become ready")
	}
	if len(m.owned) != 1 || filepath.Base(m.owned[0].cmd.Path) != "ollama" || !filepath.IsAbs(m.owned[0].cmd.Path) {
		t.Fatal("did not start resolved Ollama executable")
	}
}
