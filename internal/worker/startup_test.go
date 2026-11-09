package worker

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

// This is a real gRPC transport and real SDK client/worker, not a fake Start.
// In particular Dial/GetSystemInfo succeeds before Worker.Start blocks.
type startupServer struct {
	workflowservice.UnimplementedWorkflowServiceServer
	blockNamespace                          bool
	retryNamespace                          bool
	namespaceEntered                        chan struct{}
	namespaceExited                         chan struct{}
	connectionClosed                        chan struct{}
	systemInfo, namespace, polls, shutdowns atomic.Int32
}

func (s *startupServer) GetSystemInfo(context.Context, *workflowservice.GetSystemInfoRequest) (*workflowservice.GetSystemInfoResponse, error) {
	s.systemInfo.Add(1)
	return &workflowservice.GetSystemInfoResponse{Capabilities: &workflowservice.GetSystemInfoResponse_Capabilities{}}, nil
}

func (s *startupServer) DescribeNamespace(ctx context.Context, _ *workflowservice.DescribeNamespaceRequest) (*workflowservice.DescribeNamespaceResponse, error) {
	if s.namespace.Add(1) == 1 {
		close(s.namespaceEntered)
	}
	if s.retryNamespace {
		return nil, status.Error(codes.Unavailable, "retry startup")
	}
	if s.blockNamespace {
		<-ctx.Done()
		close(s.namespaceExited)
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	return &workflowservice.DescribeNamespaceResponse{}, nil
}

func (s *startupServer) PollWorkflowTaskQueue(ctx context.Context, _ *workflowservice.PollWorkflowTaskQueueRequest) (*workflowservice.PollWorkflowTaskQueueResponse, error) {
	s.polls.Add(1)
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

func (s *startupServer) PollActivityTaskQueue(ctx context.Context, _ *workflowservice.PollActivityTaskQueueRequest) (*workflowservice.PollActivityTaskQueueResponse, error) {
	s.polls.Add(1)
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

func (s *startupServer) ShutdownWorker(context.Context, *workflowservice.ShutdownWorkerRequest) (*workflowservice.ShutdownWorkerResponse, error) {
	s.shutdowns.Add(1)
	return &workflowservice.ShutdownWorkerResponse{}, nil
}

func (s *startupServer) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }
func (s *startupServer) HandleRPC(context.Context, stats.RPCStats)                       {}
func (s *startupServer) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}
func (s *startupServer) HandleConn(_ context.Context, event stats.ConnStats) {
	if _, ok := event.(*stats.ConnEnd); ok {
		close(s.connectionClosed)
	}
}

func serveStartup(t *testing.T, block, retry bool) (*startupServer, string, *grpc.Server) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &startupServer{
		blockNamespace: block, retryNamespace: retry,
		namespaceEntered: make(chan struct{}), namespaceExited: make(chan struct{}), connectionClosed: make(chan struct{}),
	}
	server := grpc.NewServer(grpc.StatsHandler(s))
	workflowservice.RegisterWorkflowServiceServer(server, s)
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() { server.Stop(); <-served })
	return s, listener.Addr().String(), server
}

type countedClient struct {
	temporalclient.Client
	closes    atomic.Int32
	closeOnce sync.Once
}

func (c *countedClient) Close() {
	c.closes.Add(1)
	c.closeOnce.Do(c.Client.Close)
}

type countedWorker struct {
	temporalworker.Worker
	starts, returns, stops atomic.Int32
}

func (w *countedWorker) Start() error {
	w.starts.Add(1)
	defer w.returns.Add(1)
	return w.Worker.Start()
}
func (w *countedWorker) Stop() { w.Worker.Stop(); w.stops.Add(1) }

func awaitStartup(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestStartupCancellationRealGRPC(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "retry cancellation"} {
		t.Run(mode, func(t *testing.T) {
			s, address, server := serveStartup(t, true, mode == "retry cancellation")
			ctx, cancel := context.WithCancel(context.Background())
			wantErr := context.Canceled
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
				wantErr = context.DeadlineExceeded
			}
			defer cancel()
			var client *countedClient
			clientReady := make(chan *countedClient, 1)
			var workers []*countedWorker
			dial := func(ctx context.Context, opts temporalclient.Options) (temporalclient.Client, error) {
				c, err := temporalclient.DialContext(ctx, opts)
				if err != nil {
					return nil, err
				}
				client = &countedClient{Client: c}
				clientReady <- client
				return client, nil
			}
			factory := func(c temporalclient.Client, queue string, opts temporalworker.Options) temporalworker.Worker {
				// New requires the SDK's concrete client, not the ownership wrapper.
				w := &countedWorker{Worker: temporalworker.New(c.(*countedClient).Client, queue, opts)}
				workers = append(workers, w)
				return w
			}
			var runner *Runner
			var startErr error
			returned := make(chan struct{})
			go func() {
				defer close(returned)
				runner, startErr = start(ctx, config.Config{Model: "mock", TemporalAddress: address}, dial, factory)
			}()
			// Even on an assertion failure, unblock and join the test's caller.
			t.Cleanup(func() {
				cancel()
				server.Stop()
				select {
				case <-returned:
				default:
					// Force the SDK connection closed if a regression leaves its
					// background retry loop alive after the server is stopped.
					select {
					case c := <-clientReady:
						c.Close()
					default:
					}
				}
				awaitStartup(t, returned, "test caller cleanup")
				if runner != nil {
					_ = runner.Close()
				}
			})
			awaitStartup(t, s.namespaceEntered, "DescribeNamespace after Dial")
			if s.systemInfo.Load() != 1 {
				t.Fatal("Dial did not initialize capabilities")
			}
			if mode != "deadline" {
				cancel()
			}
			awaitStartup(t, returned, "bounded Start cancellation")
			if runner != nil || !errors.Is(startErr, wantErr) {
				t.Fatalf("runner=%v error=%v, want %v", runner, startErr, wantErr)
			}
			if mode != "retry cancellation" {
				awaitStartup(t, s.namespaceExited, "server RPC cancellation")
			}
			awaitStartup(t, s.connectionClosed, "client connection close")
			if client == nil || client.closes.Load() != 1 || len(workers) != 1 {
				t.Fatalf("client=%v workers=%d", client, len(workers))
			}
			w := workers[0]
			if w.starts.Load() != 1 || w.returns.Load() != 1 || w.stops.Load() != 1 {
				t.Fatalf("start=%d return=%d stop=%d", w.starts.Load(), w.returns.Load(), w.stops.Load())
			}
			if s.polls.Load() != 0 || s.shutdowns.Load() != 1 {
				t.Fatalf("polls=%d shutdowns=%d", s.polls.Load(), s.shutdowns.Load())
			}
		})
	}
}

func TestRuntimeCancellationPreservesShutdownRPCs(t *testing.T) {
	s, address, _ := serveStartup(t, false, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := Start(ctx, config.Config{Model: "mock", TemporalAddress: address})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	cancel()
	awaitStartup(t, r.done, "runtime shutdown")
	awaitStartup(t, s.connectionClosed, "runtime connection close")
	if s.shutdowns.Load() != 3 {
		t.Fatalf("canceled context suppressed graceful ShutdownWorker RPCs: got %d, want 3", s.shutdowns.Load())
	}
	_ = r.Close()
}
