// Package devenv checks and manages local demo dependencies.
//
// A demo should defer Close and use its lifetime context for Check. A standalone
// doctor --fix should use context.Background and intentionally NOT call Close on
// success: owned services remain running after the CLI exits (logs are files,
// not pipes). On failure it should Close. No PID files are used to adopt or kill
// services belonging to another invocation. Check calls must be serialized.
package devenv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
)

type Manager struct {
	cfg    config.Config
	http   *http.Client
	mu     sync.Mutex
	owned  []*process
	closed bool
}
type process struct {
	cmd  *exec.Cmd
	done chan struct{}
	once sync.Once
}

func New(c config.Config) *Manager { return &Manager{cfg: c, http: &http.Client{}} }

// Check prints a plain-text green/red checklist, returning errors for required
// dependencies. Temporal health uses the pinned CLI's actual gRPC health RPC,
// not a TCP-connect heuristic. Consequently temporal must be installed even
// when connecting to an existing server.
func (m *Manager) Check(ctx context.Context, fix bool, progress io.Writer) error {
	if progress == nil {
		progress = io.Discard
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return errors.New("devenv manager is closed")
	}
	var failures []error
	temporalErr := m.temporalHealth(ctx)
	if temporalErr != nil && fix {
		if bin, err := config.ResolveTool(ctx, "temporal"); err != nil {
			temporalErr = fmt.Errorf("install pinned Temporal CLI with mise install: %w", err)
		} else if err := localAddress(m.cfg.TemporalAddress); err != nil {
			temporalErr = err
		} else if temporalListening(ctx, m.cfg.TemporalAddress) {
			temporalErr = fmt.Errorf("Temporal endpoint is listening but unhealthy; refusing to replace it: %w", temporalErr)
		} else {
			host, port, _ := net.SplitHostPort(m.cfg.TemporalAddress)
			args := []string{"server", "start-dev", "--db-filename", filepath.Join(m.cfg.DataDir, "temporal.db"), "--ui-port", "8233", "--log-level", "warn", "--ip", host, "--port", port}
			temporalErr = m.start(ctx, "temporal", bin, args, nil)
			if temporalErr == nil {
				temporalErr = waitReady(ctx, func() error { return m.temporalHealth(ctx) })
			}
		}
	}
	checklist(progress, "Temporal (gRPC health via CLI)", temporalErr)
	if temporalErr != nil {
		failures = append(failures, temporalErr)
	} else if fix {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		bin, err := config.ResolveTool(c, "temporal")
		var out []byte
		if err == nil {
			out, err = exec.CommandContext(c, bin, "operator", "search-attribute", "create", "--address", m.cfg.TemporalAddress, "--name", "HarnessStatus", "--type", "Keyword").CombinedOutput()
		}
		cancel()
		if err != nil {
			fmt.Fprintf(progress, "[WARN] HarnessStatus registration (best effort; may already exist): %v %s\n", err, strings.TrimSpace(string(out)))
		} else {
			fmt.Fprintln(progress, "[GREEN] HarnessStatus search attribute")
		}
	}
	if m.cfg.Model == "mock" {
		fmt.Fprintln(progress, "[GREEN] Ollama skipped (HARNESS_MODEL=mock)")
		// Registration is best effort, but cancellation of Check itself is not.
		// In particular, owned services may already be stopping on ctx.Done().
		return errors.Join(append(failures, ctx.Err())...)
	}
	present, err := m.modelPresent(ctx)
	if err != nil && fix {
		// Never replace a reachable but unhealthy service or start against a remote host.
		if isConnectionFailure(err) {
			if bin, e := config.ResolveTool(ctx, "ollama"); e != nil {
				err = fmt.Errorf("Ollama missing: %s: %w", ollamaInstall(), e)
			} else if e := localAddress(strings.TrimPrefix(strings.TrimPrefix(m.baseURL(), "http://"), "https://")); e != nil {
				err = e
			} else {
				err = m.start(ctx, "ollama", bin, []string{"serve"}, []string{"OLLAMA_HOST=" + m.cfg.OllamaHost})
				if err == nil {
					err = waitReady(ctx, func() error { var e error; present, e = m.modelPresent(ctx); return e })
				}
			}
		}
	}
	checklist(progress, "Ollama API", err)
	if err != nil {
		fmt.Fprintln(progress, ollamaInstall())
		failures = append(failures, err)
	} else {
		if !present {
			if fix {
				err = m.pull(ctx, progress)
			} else {
				err = fmt.Errorf("model %q missing; run doctor --fix", m.cfg.Model)
			}
		}
		checklist(progress, "Model "+m.cfg.Model, err)
		if err != nil {
			failures = append(failures, err)
		}
	}
	if ctx.Err() != nil {
		failures = append(failures, ctx.Err())
	}
	return errors.Join(failures...)
}
func checklist(w io.Writer, label string, err error) {
	if err != nil {
		fmt.Fprintf(w, "[RED] %s: %v\n", label, err)
	} else {
		fmt.Fprintf(w, "[GREEN] %s\n", label)
	}
}
func ollamaInstall() string {
	if runtime.GOOS == "darwin" {
		return "Install Ollama: brew install ollama"
	}
	return "Install Ollama: curl -fsSL https://ollama.com/install.sh | sh"
}
func localAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid local service address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("refusing to start local service for non-loopback address %q", address)
	}
	return nil
}

// A TCP probe is used only to avoid replacing an unhealthy existing listener,
// never to establish health.
func temporalListening(ctx context.Context, address string) bool {
	c, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
func isConnectionFailure(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}
func (m *Manager) temporalHealth(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	bin, err := config.ResolveTool(c, "temporal")
	if err != nil {
		return fmt.Errorf("Temporal health: %w", err)
	}
	out, err := exec.CommandContext(c, bin, "operator", "cluster", "health", "--address", m.cfg.TemporalAddress).CombinedOutput()
	if err != nil {
		return fmt.Errorf("Temporal health: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
func waitReady(ctx context.Context, check func() error) error {
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		last = check()
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("service readiness timeout: %w", last)
		case <-ticker.C:
		}
	}
}
func (m *Manager) baseURL() string {
	s := strings.TrimRight(m.cfg.OllamaHost, "/")
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return s
}
func (m *Manager) modelPresent(ctx context.Context) (bool, error) {
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, m.baseURL()+"/api/tags", nil)
	if err != nil {
		return false, err
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("Ollama tags: HTTP %d", resp.StatusCode)
	}
	var tags struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&tags); err != nil {
		return false, err
	}
	want := m.cfg.Model
	if !strings.Contains(want, ":") {
		want += ":latest"
	}
	for _, v := range tags.Models {
		if v.Name == m.cfg.Model || v.Name == want || v.Model == m.cfg.Model || v.Model == want {
			return true, nil
		}
	}
	return false, nil
}
func (m *Manager) pull(ctx context.Context, w io.Writer) error {
	fmt.Fprintf(w, "[WARN] Pulling %s may download several GB; this can take a while.\n", m.cfg.Model)
	body, _ := json.Marshal(map[string]any{"model": m.cfg.Model, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL()+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Ollama pull: HTTP %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	success := false
	for scanner.Scan() {
		var p struct {
			Status    string `json:"status"`
			Error     string `json:"error"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &p); err != nil {
			return err
		}
		if p.Error != "" {
			return errors.New(p.Error)
		}
		fmt.Fprintf(w, "  %s %d/%d bytes\n", p.Status, p.Completed, p.Total)
		if p.Status == "success" {
			success = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !success {
		return errors.New("Ollama pull ended without success")
	}
	return nil
}

// start gives every owned service a separate process group and file-backed logs.
func (m *Manager) start(ctx context.Context, name, bin string, args, env []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("devenv manager is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Join(m.cfg.DataDir, "logs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Start()
	_ = log.Close()
	if err != nil {
		return err
	}
	p := &process{cmd: cmd, done: make(chan struct{})}
	m.owned = append(m.owned, p)
	go func() {
		_ = cmd.Wait()
		// A service that exits unexpectedly must not leave its descendants behind.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		close(p.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			p.stop()
		case <-p.done:
		}
	}()
	return nil
}
func (p *process) stop() {
	p.once.Do(func() {
		select {
		case <-p.done:
			return
		default:
		}
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
		// Kill the group after a grace period even if the group leader exits first:
		// descendants may ignore SIGTERM.
		select {
		case <-p.done:
			return // Wait already cleaned up the remaining group.
		case <-time.After(300 * time.Millisecond):
		}
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	})
}

// Close stops only groups started by this manager; it is idempotent.
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	owned := append([]*process(nil), m.owned...)
	m.mu.Unlock()
	for _, p := range owned {
		p.stop()
	}
	return nil
}
