//go:build darwin || linux

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const processProbe = "HARNESS_PROCESS_PROBE"

// These ordinary regressions use only owned copies of the test binary, never
// Temporal or production services. The intermediate parent must itself fail.
func TestProcessGate(t *testing.T) {
	for _, mode := range []string{"normal", "kill", "fail-early", "fail-shutdown", "race-normal", "race-kill", "unexpected-kill", "exit-before-kill", "signal-error", "esrch"} {
		t.Run(mode, func(t *testing.T) { checkProcessGate(t, mode) })
	}
}

func checkProcessGate(t *testing.T, mode string) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bin, err := os.Executable()
	must(t, err)
	cmd := exec.CommandContext(ctx, bin, "-test.run=^TestProcessGateHelper$", "-test.timeout=15s", "-test.v")
	cmd.Env = withEnv(os.Environ(), map[string]string{processProbe: mode, "HARNESS_PROBE_DIR": dir,
		// A hostile inherited race configuration must NOT hide worker diagnostics.
		"GORACE": "log_path=" + filepath.Join(dir, "hidden-races") + " exitcode=0 halt_on_error=1"})
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("probe timed out: %s", out)
	}
	wantPass := mode == "normal" || mode == "kill" || mode == "esrch"
	if (err == nil) != wantPass {
		t.Fatalf("mode=%s parent exit=%v wantPass=%v\n%s", mode, err, wantPass, out)
	}
	want := ""
	switch {
	case strings.Contains(mode, "race"):
		want = "worker race diagnostic"
	case mode == "signal-error":
		want = "operation not permitted"
	case !wantPass:
		want = "unexpected process exit"
	}
	if want != "" && !strings.Contains(string(out), want) {
		t.Fatalf("missing %q in parent result:\n%s", want, out)
	}
	if mode == "signal-error" || mode == "esrch" {
		if string(readFile(t, filepath.Join(dir, "cleanup-complete"))) != "joined worker and orphan" {
			t.Fatal("remaining cleanup did not finish")
		}
	}
	hidden, err := filepath.Glob(filepath.Join(dir, "hidden-races*"))
	must(t, err)
	if len(hidden) != 0 {
		t.Fatalf("child inherited external GORACE log_path: %v", hidden)
	}
}

func TestProcessGateHelper(t *testing.T) {
	mode := os.Getenv(processProbe)
	if mode == "" {
		t.Skip("owned regression parent only")
	}
	dir := os.Getenv("HARNESS_PROBE_DIR")
	start := func(name, behavior string) *ownedProcess {
		bin, err := os.Executable()
		must(t, err)
		cmd := exec.Command(bin, "-test.run=^TestProcessLeafHelper$", "-test.timeout=12s")
		ready := filepath.Join(dir, name+".ready")
		cmd.Env = withEnv(os.Environ(), map[string]string{processProbe: behavior, "HARNESS_PROBE_READY": ready, "GORACE": workerGORACE()})
		p := startProcess(t, cmd, filepath.Join(dir, name+".log"))
		eventually(t, 5*time.Second, "probe ready", func() (bool, string) { _, err := os.Stat(ready); return err == nil, fmt.Sprint(err) })
		return p
	}
	p := start("worker", mode)
	switch mode {
	case "kill", "race-kill", "realrace-kill":
		must(t, p.stop(syscall.SIGKILL))
	case "unexpected-kill":
		must(t, syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL))
		// Registered cleanup must reject a kill not requested through stop.
	case "fail-early", "exit-before-kill":
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			t.Fatal("leaf did not exit")
		}
		if mode == "exit-before-kill" {
			must(t, p.stop(syscall.SIGKILL))
		}
	case "signal-error", "esrch":
		orphan := start("orphan", "normal")
		// Deterministically reproduce the reaped-before-done window. The real child
		// is reaped first, but the proxy waiter publishes completion only after the
		// injected signal result. No unowned PID is ever signaled.
		must(t, p.stop(syscall.SIGTERM))
		proxy := &ownedProcess{cmd: p.cmd, done: make(chan struct{}), output: p.output,
			grace: time.Second, join: time.Second}
		proxy.signal = func(pid int, sig syscall.Signal) error {
			if pid != -p.cmd.Process.Pid || sig != syscall.SIGTERM {
				t.Error("unexpected signal target")
			}
			go func() { time.Sleep(50 * time.Millisecond); close(proxy.done) }()
			if mode == "esrch" {
				return syscall.ESRCH
			}
			return syscall.EPERM
		}
		stopThenCleanup(t, proxy, func() {
			select {
			case <-proxy.done:
			default:
				t.Error("ESRCH/signal error skipped waiter join")
			}
			if err := orphan.stop(syscall.SIGKILL); err != nil {
				t.Error(err)
				return
			}
			must(t, os.WriteFile(filepath.Join(dir, "cleanup-complete"), []byte("joined worker and orphan"), 0600))
		})
	}
	// Normal/failing/race-reporting shutdown is deliberately left to the same
	// registered cleanup that gates replacement and Stop production workers.
}

func TestProcessLeafHelper(t *testing.T) {
	mode := os.Getenv(processProbe)
	ready := os.Getenv("HARNESS_PROBE_READY")
	if mode == "" || ready == "" {
		t.Skip("owned regression leaf only")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	if strings.HasPrefix(mode, "realrace-") {
		var value int
		var wg sync.WaitGroup
		wg.Add(2)
		for i := 0; i < 2; i++ {
			go func() {
				defer wg.Done()
				for j := 0; j < 10000; j++ {
					value++
				}
			}()
		}
		wg.Wait()
		fmt.Println(value)
	} else if strings.HasPrefix(mode, "race-") {
		// Put a split marker in the middle, outside BOTH retained head and tail.
		fmt.Print(strings.Repeat("before\n", captureLimit))
		fmt.Print("WARNING: DATA ")
		fmt.Print("RACE\nsynthetic detector report for parent-gate regression\n")
	}
	if strings.Contains(mode, "race") {
		fmt.Print(strings.Repeat("after\n", captureLimit))
	}
	must(t, os.WriteFile(ready, []byte("ready"), 0600))
	if mode == "fail-early" {
		os.Exit(23)
	}
	if mode == "exit-before-kill" {
		return
	}
	<-ctx.Done()
	if mode == "fail-shutdown" {
		os.Exit(24)
	}
}

func TestProcessOutputBoundedRaceLatch(t *testing.T) {
	o := new(processOutput)
	o.Write([]byte(strings.Repeat("x", captureLimit*4)))
	o.Write([]byte("WARNING: DATA "))
	o.Write([]byte("RACE\n"))
	o.Write([]byte(strings.Repeat("y", captureLimit*4)))
	out, race := o.snapshot()
	if !race || strings.Contains(out, string(raceMarker)) || len(out) > 2*captureLimit+100 {
		t.Fatalf("race=%v capture bytes=%d; middle marker must be latched, not retained", race, len(out))
	}
}
