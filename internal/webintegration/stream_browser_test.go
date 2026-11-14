//go:build integration

package webintegration_test

// Opt-in real executable stack for agent-browser / independent visual review.
// The runner owns isolated Postgres/Temporal; this test owns only its 3 children.
import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestStreamBrowserRuntime(t *testing.T) {
	dir := os.Getenv("WEBCHAT_BROWSER_RUNTIME_DIR")
	if dir == "" {
		t.Skip("opt-in interactive real-stack browser runtime")
	}
	require.True(t, filepath.IsAbs(dir))
	require.NoError(t, os.MkdirAll(dir, 0700))
	f := setup(t)
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	// Earlier suite commands cannot leak into this scoped browser execution.
	_, err = f.pool.Exec(f.ctx, "UPDATE commands SET next_attempt_at=clock_timestamp()+interval '1 day' WHERE result_state='pending'")
	require.NoError(t, err)
	// Explicit synthetic HISTORY fixture only, not model/turn-completion proof.
	c := f.conversation(t, "qwen3.5:9b")
	for i := 0; i < 25; i++ {
		text := fmt.Sprintf("Paging fixture %02d — synthetic saved user record.\n\nThis paragraph gives the transcript a stable reading position. Scroll up and load the previous page without jumping to the newest message.", i)
		if i == 0 {
			text = "Paging and Markdown safety fixture — synthetic\n\n![remote beacon](http://127.0.0.1:9/must-not-load?secret=fixture)\n\n[Unsafe](javascript:alert%281%29)\n\n<svg onload=alert(1)><image href=\"http://127.0.0.1:9/svg\" /></svg>\n\n[User-activated source](https://example.com/source)"
		}
		cmd := f.command(t, c, text)
		_, err = f.pool.Exec(f.ctx, "UPDATE commands SET next_attempt_at=clock_timestamp()+interval '1 day' WHERE id=$1", cmd.CommandID)
		require.NoError(t, err)
	}
	port := func() string {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, e)
		addr := l.Addr().String()
		l.Close()
		return addr
	}
	apiAddr, webAddr := port(), port()
	origin := "http://" + webAddr
	b := make([]byte, 32)
	_, err = rand.Read(b)
	require.NoError(t, err)
	env := append(os.Environ(), "WEBCHAT_DATABASE_URL="+os.Getenv("WEBCHAT_TEST_DATABASE_URL"), "WEBCHAT_TEMPORAL_ADDRESS="+f.address, "WEBCHAT_API_LISTEN="+apiAddr, "WEBCHAT_API_URL=http://"+apiAddr, "WEBCHAT_BROWSER_ORIGIN="+origin, "WEBCHAT_PROXY_SECRET="+hex.EncodeToString(b), "WEBCHAT_ATTACHMENT_ROOT="+filepath.Join(t.TempDir(), "attachments"))
	bin := t.TempDir()
	for _, name := range []string{"web-api", "web-worker"} {
		cmd := exec.CommandContext(f.ctx, "go", "build", "-p", "1", "-o", filepath.Join(bin, name), "./cmd/"+name)
		cmd.Dir = root
		output, e := cmd.CombinedOutput()
		require.NoError(t, e, string(output))
	}
	start := func(name string, args ...string) {
		log, e := os.OpenFile(filepath.Join(dir, filepath.Base(name)+".log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		require.NoError(t, e)
		cmd := exec.Command(name, args...)
		cmd.Env = env
		cmd.Dir = filepath.Join(root, "web")
		cmd.Stdout = log
		cmd.Stderr = log
		require.NoError(t, cmd.Start())
		t.Logf("owned browser child %s pid=%d", name, cmd.Process.Pid)
		t.Cleanup(func() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
			log.Close()
		})
	}
	start(filepath.Join(bin, "web-worker"))
	start(filepath.Join(bin, "web-api"))
	_, wp, _ := net.SplitHostPort(webAddr)
	start("node", filepath.Join(root, "web/node_modules/next/dist/bin/next"), "start", "--hostname", "127.0.0.1", "--port", wp)
	httpClient := &http.Client{Timeout: 2 * time.Second}
	require.Eventually(t, func() bool {
		req, _ := http.NewRequest("GET", "http://"+apiAddr+"/readyz", nil)
		req.Header.Set("X-Webchat-Secret", hex.EncodeToString(b))
		res, e := httpClient.Do(req)
		if e != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	}, 30*time.Second, 200*time.Millisecond)
	require.Eventually(t, func() bool {
		res, e := httpClient.Get(origin)
		if e != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	}, 30*time.Second, 200*time.Millisecond)
	ready, _ := json.Marshal(map[string]string{"url": origin, "fixtureConversationId": string(c.ID), "fixture": "25 synthetic accepted user records; not model output", "stop": "create a file named done in this directory; no shared services are stopped"})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ready.json"), ready, 0600))
	t.Logf("Browser ready at %s; receipt %s", origin, filepath.Join(dir, "ready.json"))
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-f.ctx.Done():
			t.Fatal("browser runtime expired after 12 minutes")
		case <-ticker.C:
			if _, e := os.Stat(filepath.Join(dir, "done")); e == nil {
				return
			}
		}
	}
}
