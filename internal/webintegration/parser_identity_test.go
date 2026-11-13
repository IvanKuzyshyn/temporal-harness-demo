//go:build integration

package webintegration_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Actual executable API + worker, SQL, Temporal and Docker; inference alone is
// the worker's explicit test mock. The only mutable tag belongs to this test.
func TestParserIdentityExecutableConsumers(t *testing.T) {
	if os.Getenv("WEBCHAT_TEST_PARSERS") != "1" {
		t.Skip("explicit isolated parser container gate")
	}
	f := setup(t)
	dir := t.TempDir()
	imageA := os.Getenv("WEBCHAT_PARSER_IMAGE_ID")
	require.Regexp(t, `^sha256:[a-f0-9]{64}$`, imageA)
	docker, err := exec.LookPath("docker")
	require.NoError(t, err)
	docker, err = filepath.Abs(docker)
	require.NoError(t, err)
	invoke := func(args ...string) string {
		out, e := exec.CommandContext(f.ctx, docker, args...).CombinedOutput()
		require.NoError(t, e, string(out))
		return strings.TrimSpace(string(out))
	}
	owner := uuid.NewString()
	alias := "webchat-parser-r1-" + owner + ":owned"
	invoke("tag", imageA, alias)
	// Retain this unique tag/image as explicit evidence; never delete the shared
	// content-addressed A or a cache used by another home, even on test failure.
	t.Logf("owned alias=%s original=%s (image retained)", alias, imageA)
	audit := filepath.Join(dir, "docker-audit")
	wrapper := fmt.Sprintf(`#!/bin/sh
set -eu
if [ "$1" = create ]; then
 id=$(%q "$@")
 %q inspect --format '{{.Id}} {{.Image}} {{.Config.Image}} {{json .HostConfig}}' "$id" >> "$PARSER_AUDIT.$PARSER_CONSUMER"
 printf '%%s\n' "$id"
else
 exec %q "$@"
fi
`, docker, docker, docker)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(wrapper), 0700))
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	for _, name := range []string{"web-api", "web-worker"} {
		cmd := exec.CommandContext(f.ctx, "go", "build", "-p", "1", "-o", filepath.Join(dir, name), "./cmd/"+name)
		cmd.Dir = root
		out, e := cmd.CombinedOutput()
		require.NoError(t, e, string(out))
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	secretBytes := make([]byte, 32)
	_, err = rand.Read(secretBytes)
	require.NoError(t, err)
	secret := hex.EncodeToString(secretBytes)
	env := append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "PARSER_AUDIT="+audit,
		"WEBCHAT_DATABASE_URL="+os.Getenv("WEBCHAT_TEST_DATABASE_URL"), "WEBCHAT_TEMPORAL_ADDRESS="+f.address,
		"WEBCHAT_API_LISTEN="+addr, "WEBCHAT_BROWSER_ORIGIN=http://localhost:3000", "WEBCHAT_PROXY_SECRET="+secret,
		"WEBCHAT_ATTACHMENT_ROOT="+filepath.Join(dir, "attachments"), "WEBCHAT_PARSER_IMAGE_ID="+imageA)
	// Earlier cases' durable commands must not be picked up by this test's real
	// dispatcher; this is the same isolation used by the opt-in browser runtime.
	_, err = f.pool.Exec(f.ctx, "UPDATE commands SET next_attempt_at=clock_timestamp()+interval '1 day' WHERE result_state='pending'")
	require.NoError(t, err)
	for _, consumer := range []string{"worker", "api"} {
		args := []string{}
		if consumer == "worker" {
			args = append(args, "-test-mock")
		}
		cmd := exec.Command(filepath.Join(dir, "web-"+consumer), args...)
		cmd.Env = append(append([]string{}, env...), "PARSER_CONSUMER="+consumer)
		log, e := os.Create(filepath.Join(dir, consumer+".log"))
		require.NoError(t, e)
		cmd.Stdout = log
		cmd.Stderr = log
		require.NoError(t, cmd.Start())
		t.Logf("owned %s pid=%d", consumer, cmd.Process.Pid)
		t.Cleanup(func() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("owned executable failed graceful exit")
			}
			require.NoError(t, log.Close())
			if t.Failed() {
				b, _ := os.ReadFile(log.Name())
				t.Log(string(b))
			}
		})
		if consumer == "worker" {
			// Identity binding is the subject here, not simultaneous first-time
			// filesystem layout creation. Join worker startup before API startup.
			require.Eventually(t, func() bool {
				b, e := os.ReadFile(log.Name())
				return e == nil && strings.Count(string(b), "Started Worker") == 3
			}, 20*time.Second, 50*time.Millisecond)
		}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	request := func(method, path, typ string, b io.Reader) (*http.Response, error) {
		req, e := http.NewRequestWithContext(f.ctx, method, "http://"+addr+path, b)
		if e != nil {
			return nil, e
		}
		req.Header.Set("X-Webchat-Secret", secret)
		if typ != "" {
			req.Header.Set("Content-Type", typ)
		}
		return client.Do(req)
	}
	require.Eventually(t, func() bool {
		res, e := request("GET", "/readyz", "", nil)
		if e != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	}, 40*time.Second, 100*time.Millisecond)
	// Move ONLY this test's alias to a distinct, owned image after both consumers
	// initialized. A retains its runner-owned build tag throughout the race.
	cmd := exec.CommandContext(f.ctx, docker, "build", "--iidfile", filepath.Join(dir, "b.id"), "-t", alias, "-")
	// A deliberately different empty image needs no registry/base lookup and
	// must never be executed by either parser consumer.
	cmd.Stdin = strings.NewReader("FROM scratch\nLABEL webchat.test.owner=\"" + owner + "\"\n")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	// Our exclusive alias is safe to inspect; iidfile can be an unroutable
	// config digest on containerd. Neither A nor B uses any shared build alias.
	imageB := invoke("image", "inspect", "--format", "{{.Id}}", alias)
	require.NotEqual(t, imageA, imageB)
	require.Equal(t, owner, invoke("image", "inspect", "--format", `{{index .Config.Labels "webchat.test.owner"}}`, imageB))
	t.Logf("owned alias now B=%s; both running consumers remain configured A=%s", imageB, imageA)
	c := f.conversation(t, "qwen3.5:9b")
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "immutable.pdf")
	require.NoError(t, err)
	_, err = part.Write(analysisPDF("IMMUTABLE R1 DOCUMENT"))
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	res, err := request("POST", "/conversations/"+string(c.ID)+"/attachments", mw.FormDataContentType(), &body)
	require.NoError(t, err)
	response, err := io.ReadAll(res.Body)
	res.Body.Close()
	require.NoError(t, err)
	require.Equal(t, 201, res.StatusCode, string(response))
	var saved struct {
		Attachment struct {
			ID chat.AttachmentID `json:"id"`
		} `json:"attachment"`
	}
	require.NoError(t, json.Unmarshal(response, &saved))
	require.NotEmpty(t, saved.Attachment.ID)
	command := f.attachmentCommand(t, c, saved.Attachment.ID)
	_, err = f.gateway.Submit(f.ctx, command)
	require.NoError(t, err)
	var pending *chat.WebApproval
	require.Eventually(t, func() bool { pending = f.approvalState(t, c).PendingApproval; return pending != nil }, 15*time.Second, 20*time.Millisecond)
	_, err = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, pending, chat.ApprovalOnce))
	require.NoError(t, err)
	f.completed(t, command)
	var extracted string
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT extracted_text FROM attachments WHERE id=$1", saved.Attachment.ID).Scan(&extracted))
	require.Contains(t, extracted, "IMMUTABLE R1 DOCUMENT")
	for _, consumer := range []string{"api", "worker"} {
		b, e := os.ReadFile(audit + "." + consumer)
		require.NoError(t, e, "consumer must actually create a parser after alias movement")
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			fields := strings.SplitN(line, " ", 4)
			require.Len(t, fields, 4)
			require.Equal(t, imageA, fields[1], "actual container image")
			require.Equal(t, imageA, fields[2], "create argument must be immutable")
			t.Logf("%s actual docker create/inspect: %s", consumer, line)
			inspect := exec.CommandContext(f.ctx, docker, "inspect", fields[0])
			missing, e := inspect.CombinedOutput()
			require.Error(t, e)
			require.Contains(t, strings.ToLower(string(missing)), "no such object", "joined helper cleanup")
		}
	}
	require.Equal(t, imageB, invoke("image", "inspect", "--format", "{{.Id}}", alias), "neither consumer retags alias to mask selection")
}
