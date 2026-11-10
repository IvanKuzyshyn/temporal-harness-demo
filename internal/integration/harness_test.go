//go:build darwin || linux

// Package integration tests real Temporal durability with the explicit mock LLM.
// Ordinary go test never resolves tools or starts services.
package integration

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	harnessclient "github.com/IvanKuzyshyn/temporal-harness-demo/internal/client"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"
)

const helperGuard = "HARNESS_DURABILITY_WORKER"

// TestWorkerHelper runs only in an explicitly guarded subprocess of this binary.
// It uses the production registration/model selection, not a replacement workflow.
func TestWorkerHelper(t *testing.T) {
	if os.Getenv(helperGuard) != "1" {
		t.Skip("owned subprocess only")
	}
	address, ready := os.Getenv("TEMPORAL_ADDRESS"), os.Getenv("HARNESS_DURABILITY_READY")
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port == "7233" || port == "8233" || !filepath.IsAbs(ready) || os.Getenv("HARNESS_MODEL") != "mock" {
		t.Fatal("refusing unguarded worker configuration")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	// Start's context owns runner lifetime. Parent readiness is bounded to 35s;
	// cleanup signals this context and joins the helper even on startup failure.
	r, err := worker.Start(ctx, config.Config{Model: "mock", TemporalAddress: address})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
}

type ownedProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // read only after done is closed
	log  string
}

func startProcess(t *testing.T, cmd *exec.Cmd, log string) *ownedProcess {
	t.Helper()
	f, err := os.Create(log)
	must(t, err)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		f.Close()
		t.Fatal(err)
	}
	p := &ownedProcess{cmd: cmd, done: make(chan struct{}), log: log}
	go func() {
		p.err = cmd.Wait()
		_ = f.Close()
		close(p.done)
	}()
	t.Cleanup(func() {
		p.stop(t, syscall.SIGTERM)
		if t.Failed() {
			b, _ := os.ReadFile(log)
			if len(b) > 16000 {
				b = b[len(b)-16000:]
			}
			t.Logf("owned process log %s:\n%s", log, b)
		}
	})
	return p
}

func (p *ownedProcess) stop(t *testing.T, sig syscall.Signal) {
	t.Helper()
	select {
	case <-p.done:
		return
	default:
	}
	// This process group was created by startProcess, never discovered by name.
	must(t, syscall.Kill(-p.cmd.Process.Pid, sig))
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			t.Errorf("owned process %d did not join", p.cmd.Process.Pid)
		}
	}
}

type liveServer struct {
	address string
	root    string
	cli     string
	sdk     temporalclient.Client
	client  *harnessclient.Client
}

func newServer(t *testing.T) *liveServer {
	t.Helper()
	if os.Getenv("HARNESS_INTEGRATION") != "1" {
		t.Skip("live Temporal disabled; use scripts/test-integration.sh")
	}
	if os.Getenv("HARNESS_MODEL") != "mock" {
		t.Fatal("live suite requires explicit HARNESS_MODEL=mock (no Ollama access)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cli, err := config.ResolveTool(ctx, "temporal")
	must(t, err)
	root := t.TempDir()
	// Hold all reservations until every port is chosen. start-dev cannot inherit
	// listeners: a bind race fails this test, never attaches it to another server.
	var listeners []net.Listener
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	var ports []string
	for len(ports) < 4 {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		must(t, err)
		port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
		if port == "7233" || port == "8233" {
			l.Close()
			continue
		}
		listeners = append(listeners, l)
		ports = append(ports, port)
	}
	for _, l := range listeners {
		must(t, l.Close())
	}
	// A private random namespace is a readiness/ownership sentinel. Production
	// still uses this server's default namespace. Never start workers merely
	// because a potentially racing listener responds to a generic health check.
	sentinel := "durability-" + strings.ToLower(rand.Text())
	args := []string{"server", "start-dev", "--namespace", sentinel, "--ip", "127.0.0.1", "--port", ports[0], "--ui-ip", "127.0.0.1", "--ui-port", ports[1], "--http-port", ports[2], "--metrics-port", ports[3], "--ui-disable-news-fetch", "--db-filename", filepath.Join(root, "temporal.sqlite")}
	p := startProcess(t, exec.Command(cli, args...), filepath.Join(root, "server.log"))
	s := &liveServer{address: "127.0.0.1:" + ports[0], root: root}
	t.Logf("REAL Temporal %s, owned PID=%d gRPC=%s UI=127.0.0.1:%s HTTP=%s metrics=%s tempDB=%s; MOCK LLM", cli, p.cmd.Process.Pid, s.address, ports[1], ports[2], ports[3], filepath.Join(root, "temporal.sqlite"))
	eventually(t, 40*time.Second, "owned Temporal server health", func() (bool, string) {
		select {
		case <-p.done:
			t.Fatalf("owned server exited: %v (see %s)", p.err, p.log)
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		c, err := temporalclient.DialContext(ctx, temporalclient.Options{HostPort: s.address})
		if err != nil {
			return false, err.Error()
		}
		_, err = c.CheckHealth(ctx, nil)
		if err == nil {
			_, err = c.WorkflowService().DescribeNamespace(ctx, &workflowservice.DescribeNamespaceRequest{Namespace: sentinel})
		}
		if err != nil {
			c.Close()
			return false, err.Error()
		}
		s.sdk = c
		return true, ""
	})
	t.Cleanup(s.sdk.Close)
	s.reconnect(t)
	// Build CLI once, using no dependency-starting commands.
	s.cli = filepath.Join(root, "harness")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), time.Minute)
	defer buildCancel()
	cmd := exec.CommandContext(buildCtx, "go", "build", "-o", s.cli, "../../cmd/harness")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build CLI: %v\n%s", err, b)
	}
	return s
}

func (s *liveServer) reconnect(t *testing.T) {
	t.Helper()
	if s.client != nil {
		s.client.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := harnessclient.Dial(ctx, s.address)
	must(t, err)
	s.client = c
	t.Cleanup(c.Close)
}

func (s *liveServer) startWorker(t *testing.T) *ownedProcess {
	t.Helper()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	bin, err := os.Executable()
	must(t, err)
	cmd := exec.Command(bin, "-test.run=^TestWorkerHelper$", "-test.timeout=5m")
	cmd.Env = withEnv(os.Environ(), map[string]string{helperGuard: "1", "TEMPORAL_ADDRESS": s.address, "HARNESS_MODEL": "mock", "HARNESS_DURABILITY_READY": ready, "GOTOOLCHAIN": "local", "GOWORK": "off", "GOFLAGS": ""})
	p := startProcess(t, cmd, filepath.Join(dir, "worker.log"))
	eventually(t, 35*time.Second, "production worker startup", func() (bool, string) {
		select {
		case <-p.done:
			t.Fatalf("worker exited: %v", p.err)
		default:
		}
		_, err := os.Stat(ready)
		return err == nil, fmt.Sprint(err)
	})
	return p
}

func (s *liveServer) state(t *testing.T, id string, predicate func(workflows.State) bool) workflows.State {
	t.Helper()
	var state workflows.State
	eventually(t, 45*time.Second, "authoritative state for "+id, func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var err error
		state, err = s.client.Query(ctx, id)
		if err != nil {
			// A restarting worker may not yet service Queries. Never interpret an
			// error or a zero State as settlement.
			return false, err.Error()
		}
		return predicate(state), fmt.Sprintf("%+v", state)
	})
	return state
}

// Register before workers/command ownership so LIFO cleanup first stops all
// writers, then terminates the private session, then removes its temp files.
// Otherwise the next subtest's worker could resume a failed test's old workflow
// and recreate files after TempDir cleanup.
func (s *liveServer) ownSession(t *testing.T, id string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := s.sdk.TerminateWorkflow(ctx, id, "", "owned live test cleanup")
		var missing *serviceerror.NotFound
		if err != nil && !errors.As(err, &missing) {
			t.Errorf("terminate owned session %s: %v", id, err)
		}
	})
}

func (s *liveServer) submit(t *testing.T, id string, input workflows.Input, request, text string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := s.client.Submit(ctx, id, input, workflows.MessageInput{RequestID: request, Text: text, WhenBusy: "followUp"})
	must(t, err)
}

func (s *liveServer) cliWait(t *testing.T, id, until string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cli, "wait", id, "--until", until, "--timeout", "10s")
	cmd.Env = withEnv(os.Environ(), map[string]string{"TEMPORAL_ADDRESS": s.address, "HARNESS_MODEL": "mock", "HARNESS_DATA_DIR": s.root})
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI wait %s: %v\n%s", until, err, b)
	}
	return string(b)
}

func eventually(t *testing.T, timeout time.Duration, description string, check func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ok, detail := check()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %s", description, detail)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func withEnv(env []string, values map[string]string) []string {
	out := make([]string, 0, len(env)+len(values))
	for _, v := range env {
		key, _, _ := strings.Cut(v, "=")
		if _, replaced := values[key]; !replaced {
			out = append(out, v)
		}
	}
	for k, v := range values {
		out = append(out, k+"="+v)
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0700))
	must(t, os.WriteFile(path, []byte(content), 0600))
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return b
}
