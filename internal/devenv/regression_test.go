package devenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
)

type cancelWriter struct{ cancel context.CancelFunc }

func (w cancelWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "[GREEN] Temporal") {
		w.cancel()
	}
	return len(p), nil
}

func TestMockCheckCancellationAfterTemporal(t *testing.T) {
	fakeTemporal(t)
	for _, fix := range []bool{false, true} {
		t.Run(fmt.Sprint(fix), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := New(config.Config{Model: "mock"})
			defer m.Close()
			if err := m.Check(ctx, fix, cancelWriter{cancel}); !errors.Is(err, context.Canceled) {
				t.Fatalf("Check error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestMockCheckCancellationDuringRegistration(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "registering")
	script := "#!/bin/sh\nif [ \"$2\" = search-attribute ]; then\n touch \"$REGISTRATION_MARKER\"\n exec sleep 60\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "temporal"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("REGISTRATION_MARKER", marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(config.Config{Model: "mock"})
	defer m.Close()
	result := make(chan error, 1)
	go func() { result <- m.Check(ctx, true, nil) }()
	waitFor(t, "registration start", func() bool { _, err := os.Stat(marker); return err == nil })
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Check error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registration ignored cancellation")
	}
}

func waitFor(t *testing.T, label string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + label)
}

func TestOwnedDescendantCleanup(t *testing.T) {
	for _, mode := range []string{"close", "close-resistant-leader", "cancel", "leader-exit"} {
		t.Run(mode, func(t *testing.T) {
			unrelated := exec.Command("sleep", "60")
			unrelated.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := unrelated.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = syscall.Kill(-unrelated.Process.Pid, syscall.SIGKILL); _ = unrelated.Wait() }()
			dir := t.TempDir()
			pidfile := filepath.Join(dir, "descendant.pid")
			// The descendant installs SIGTERM ignore before publishing its PID, then
			// execs sleep (which retains ignored signals). The leader exits on TERM.
			script := "sh -c 'trap \"\" TERM; echo $$ > \"$PIDFILE\"; exec sleep 60' & wait"
			if mode == "close-resistant-leader" {
				// Exercise grace-period escalation as well as leader-first exit.
				script = "trap '' TERM; " + script
			}
			m := New(config.Config{DataDir: dir})
			defer m.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := m.start(ctx, "tree", "sh", []string{"-c", script}, []string{"PIDFILE=" + pidfile}); err != nil {
				t.Fatal(err)
			}
			pid := 0
			waitFor(t, "descendant readiness", func() bool {
				b, err := os.ReadFile(pidfile)
				if err != nil {
					return false
				}
				pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
				return err == nil && pid > 0
			})
			defer syscall.Kill(pid, syscall.SIGKILL)
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(pid, 0); err != nil {
				t.Fatal("descendant did not survive SIGTERM", err)
			}
			switch mode {
			case "close", "close-resistant-leader":
				_ = m.Close()
			case "cancel":
				cancel()
			case "leader-exit":
				_ = m.owned[0].cmd.Process.Kill()
			}
			waitFor(t, "descendant death", func() bool {
				if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
					return true
				}
				// On systems with delayed orphan reaping, a zombie is dead but still
				// has a PID. Do not mistake that for a running descendant.
				out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
				return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
			})
			if err := syscall.Kill(unrelated.Process.Pid, 0); err != nil {
				t.Fatal("unrelated group was stopped", err)
			}
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
