package devenv

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
)

// Opt-in integration test uses the pinned Temporal CLI, no Ollama/model download.
func TestRealTemporalLifecycle(t *testing.T) {
	if os.Getenv("HARNESS_TEST_TEMPORAL") != "1" {
		t.Skip("set HARNESS_TEST_TEMPORAL=1 to launch the pinned CLI")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	_ = l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m := New(config.Config{Model: "mock", TemporalAddress: address, DataDir: t.TempDir()})
	defer m.Close()
	var out bytes.Buffer
	if err := m.Check(ctx, true, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if len(m.owned) != 1 {
		t.Fatalf("expected one owned Temporal process, got %d", len(m.owned))
	}
	other := New(m.cfg)
	if err := other.Check(ctx, false, &out); err != nil {
		t.Fatal(err)
	}
	_ = other.Close()
	if err := m.temporalHealth(ctx); err != nil {
		t.Fatal("reuse Close killed existing server", err)
	}
	_ = m.Close()
	if err := m.temporalHealth(ctx); err == nil {
		t.Fatal("owned server survived Close")
	}
	t.Log(out.String())
}
func fakeTemporal(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "temporal"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}
func TestReuseAndMock(t *testing.T) {
	fakeTemporal(t)
	for _, model := range []string{"mock", "test:latest"} {
		t.Run(model, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/tags" {
					t.Error(r.URL.Path)
				}
				fmt.Fprint(w, `{"models":[{"name":"test:latest"}]}`)
			}))
			defer s.Close()
			m := New(config.Config{Model: model, TemporalAddress: "localhost:7233", OllamaHost: s.URL, DataDir: t.TempDir()})
			defer m.Close()
			var out bytes.Buffer
			if err := m.Check(context.Background(), true, &out); err != nil {
				t.Fatal(err, out.String())
			}
			if len(m.owned) != 0 {
				t.Fatal("started existing service")
			}
			if model == "mock" && calls != 0 {
				t.Fatal("mock contacted ollama")
			}
			if !strings.Contains(out.String(), "[GREEN]") {
				t.Fatal(out.String())
			}
		})
	}
}
func TestPull(t *testing.T) {
	fakeTemporal(t)
	pulled := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[]}`)
		case "/api/pull":
			pulled = true
			fmt.Fprintln(w, `{"status":"downloading","total":100,"completed":50}`)
			fmt.Fprintln(w, `{"status":"success"}`)
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer s.Close()
	m := New(config.Config{Model: "test", OllamaHost: s.URL, DataDir: t.TempDir()})
	defer m.Close()
	var out bytes.Buffer
	if err := m.Check(context.Background(), false, &out); err == nil {
		t.Fatal("missing model accepted")
	}
	if pulled {
		t.Fatal("pulled without fix")
	}
	if err := m.Check(context.Background(), true, &out); err != nil {
		t.Fatal(err)
	}
	if !pulled || !strings.Contains(out.String(), "several GB") || !strings.Contains(out.String(), "50/100") {
		t.Fatal(out.String())
	}
}
func TestPullErrorsAndCancellation(t *testing.T) {
	for _, body := range []string{`{"error":"disk full"}`, `{"status":"downloading"}`, `bad json`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer s.Close()
			m := New(config.Config{OllamaHost: s.URL})
			if err := m.pull(context.Background(), &bytes.Buffer{}); err == nil {
				t.Fatal("accepted failed/incomplete pull")
			}
		})
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer s.Close()
	m := New(config.Config{OllamaHost: s.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := m.pull(ctx, &bytes.Buffer{}); err == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestUnhealthyServicesAreNotReplaced(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "temporal"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unhealthy", 503) }))
	defer server.Close()
	m := New(config.Config{Model: "test", TemporalAddress: listener.Addr().String(), OllamaHost: server.URL, DataDir: t.TempDir()})
	defer m.Close()
	var out bytes.Buffer
	if err := m.Check(context.Background(), true, &out); err == nil {
		t.Fatal("unhealthy services accepted")
	}
	if len(m.owned) != 0 {
		t.Fatal("replaced unhealthy service")
	}
	if !strings.Contains(out.String(), "[RED]") || !strings.Contains(out.String(), "refusing to replace") {
		t.Fatal(out.String())
	}
}
func TestCanceledCheckAndClosedManager(t *testing.T) {
	fakeTemporal(t)
	m := New(config.Config{Model: "mock"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Check(ctx, true, nil); err == nil {
		t.Fatal("accepted canceled check")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Check(context.Background(), true, nil); err == nil {
		t.Fatal("accepted closed manager")
	}
	if err := m.start(context.Background(), "child", "sh", nil, nil); err == nil {
		t.Fatal("started on closed manager")
	}
}
func TestOwnedProcessCleanup(t *testing.T) {
	m := New(config.Config{DataDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.start(ctx, "child", "sh", []string{"-c", "sleep 60 & wait"}, nil); err != nil {
		t.Fatal(err)
	}
	p := m.owned[0]
	cancel()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("child survived cancellation")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}
