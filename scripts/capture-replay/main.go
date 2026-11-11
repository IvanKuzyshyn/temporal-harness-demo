// Command capture-replay is an explicitly opt-in local fixture recorder.
// It never connects to an existing server. Run from the repository root.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	shared "github.com/IvanKuzyshyn/temporal-harness-demo/internal/client"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type fixture struct {
	File         string `json:"file"`
	WorkflowID   string `json:"workflowId"`
	RunID        string `json:"runId"`
	WorkflowType string `json:"workflowType"`
	Events       int    `json:"events"`
	SHA256       string `json:"sha256"`
	RawSHA256    string `json:"rawSha256"`
}
type manifest struct {
	Command          string                      `json:"command"`
	CapturedAt       string                      `json:"capturedAt"`
	SourceCommit     string                      `json:"sourceCommit"`
	GoVersion        string                      `json:"goVersion"`
	SDKVersion       string                      `json:"sdkVersion"`
	TemporalVersion  string                      `json:"temporalVersion"`
	Address          string                      `json:"address"`
	UIPort           int                         `json:"uiPort"`
	Redaction        string                      `json:"redaction"`
	FirstReceipt     workflows.MessageReceipt    `json:"firstReceipt"`
	DuplicateReceipt workflows.MessageReceipt    `json:"duplicateReceipt"`
	BeforeDuplicate  workflows.State             `json:"beforeDuplicate"`
	AfterDuplicate   workflows.State             `json:"afterDuplicate"`
	FinalState       workflows.State             `json:"finalState"`
	Approvals        []workflows.PendingApproval `json:"approvals"`
	Histories        []fixture                   `json:"histories"`
}

func main() {
	opt := flag.Bool("capture", false, "opt in to an owned local server and real sample commands")
	out := flag.String("out", "internal/workflows/testdata/replay", "new output directory (must not exist)")
	flag.Parse()
	if !*opt {
		fmt.Fprintln(os.Stderr, "capture disabled; pass -capture explicitly")
		os.Exit(2)
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, 4*time.Minute)
	defer cancel()
	if err := capture(ctx, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func port() (int, error) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	p := l.Addr().(*net.TCPAddr).Port
	if p == 7233 || p == 8233 {
		return 0, errors.New("refusing reserved port")
	}
	return p, nil
}
func pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}
func capture(ctx context.Context, out string) error {
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		return fmt.Errorf("output must not exist: %s", out)
	}
	cli, err := config.ResolveTool(ctx, "temporal")
	if err != nil {
		return err
	}
	version, err := exec.CommandContext(ctx, cli, "--version").Output()
	if err != nil {
		return err
	}
	commit, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "harness-replay-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	project := filepath.Join(root, "project")
	if err = os.CopyFS(project, os.DirFS("examples/sample-project")); err != nil {
		return err
	}
	p, err := port()
	if err != nil {
		return err
	}
	ui, err := port()
	if err != nil {
		return err
	}
	if p == ui {
		return errors.New("port collision; retry capture")
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(p))
	logFile, err := os.Create(filepath.Join(root, "temporal.log"))
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(cli, "server", "start-dev", "--ip", "127.0.0.1", "--port", strconv.Itoa(p), "--ui-ip", "127.0.0.1", "--ui-port", strconv.Itoa(ui), "--ui-disable-news-fetch", "--db-filename", filepath.Join(root, "temporal.db"))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	var serverErr error
	go func() { serverErr = cmd.Wait(); close(done) }()
	defer func() {
		select {
		case <-done:
			return
		default:
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}()
	var sdk client.Client
	for {
		select {
		case <-done:
			return fmt.Errorf("owned server exited: %v", serverErr)
		default:
		}
		dialCtx, cancel := context.WithTimeout(ctx, time.Second)
		sdk, err = client.DialContext(dialCtx, client.Options{HostPort: address, Identity: "replay-capture",
			// Worker.Start uses background contexts in the pinned SDK. Bound those
			// initialization RPCs (and all later RPCs) to the capture lifetime too.
			ConnectionOptions: client.ConnectionOptions{DialOptions: []grpc.DialOption{grpc.WithUnaryInterceptor(
				func(rpcCtx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
					rpcCtx, cancel := context.WithCancel(rpcCtx)
					defer cancel()
					stop := context.AfterFunc(ctx, cancel)
					defer stop()
					if err := ctx.Err(); err != nil {
						return err
					}
					return invoke(rpcCtx, method, req, reply, cc, opts...)
				})}},
		})
		cancel()
		if err == nil {
			break
		}
		if err = pause(ctx); err != nil {
			return err
		}
	}
	defer sdk.Close()
	a := &activities.Activities{Model: mock.New()}
	for _, q := range []string{workflows.AgentsQueue, workflows.LLMQueue, workflows.SubagentsQueue} {
		w := worker.New(sdk, q, worker.Options{Identity: "replay-capture"})
		if q == workflows.LLMQueue {
			w.RegisterActivity(a.LLMStream)
		} else {
			if q == workflows.AgentsQueue {
				w.RegisterWorkflow(workflows.SessionWorkflow)
			} else {
				w.RegisterWorkflow(workflows.ExplorerWorkflow)
			}
			w.RegisterActivity(a.EmitEvent)
			w.RegisterActivity(a.AppendTranscript)
			w.RegisterActivity(a.ExecuteTool)
			w.RegisterActivity(a.GatherContext)
			w.RegisterActivity(a.NotifyApproval)
			w.RegisterActivity(a.RunCommand)
			w.RegisterActivity(a.PreflightTool)
			w.RegisterActivity(a.RecoverCommand)
		}
		if err = w.Start(); err != nil {
			return err
		}
		defer w.Stop()
	}
	m := manifest{Command: fmt.Sprintf("go run ./scripts/capture-replay -capture -out %q", out), CapturedAt: time.Now().UTC().Format(time.RFC3339), SourceCommit: strings.TrimSpace(string(commit)), GoVersion: runtime.Version(), TemporalVersion: strings.TrimSpace(string(version)), Address: address, UIPort: ui, Redaction: "Only the owned temporary root is replaced with /replay-capture in protobuf strings and decoded json/plain payload bytes. Worker identity is replay-capture at source. Workflow/run IDs, event structure and activity results are preserved; raw checksums refer to pre-redaction protojson."}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			if dep.Path == "go.temporal.io/sdk" {
				m.SDKVersion = dep.Version
			}
		}
	}
	c := shared.New(sdk)
	id := fmt.Sprintf("recorded-replay-%d", time.Now().UnixNano())
	input := workflows.Input{Model: "mock", Workdir: project, DataDir: filepath.Join(root, "data"), ContinueAfterTurns: 1, MaxTurns: 12, ApprovalTimeout: time.Minute}
	hello := workflows.MessageInput{RequestID: "recorded-hello", Text: "hello"}
	m.FirstReceipt, err = c.Submit(ctx, id, input, hello)
	if err != nil {
		return err
	}
	// Determine the first run from the actual chain, even if CAN beats Describe.
	desc, err := sdk.DescribeWorkflowExecution(ctx, id, "")
	if err != nil {
		return err
	}
	firstRun := desc.WorkflowExecutionInfo.FirstRunId
	if firstRun == "" {
		return errors.New("server omitted first run ID")
	}
	runID := func() (string, error) {
		d, e := sdk.DescribeWorkflowExecution(ctx, id, "")
		if e != nil {
			return "", e
		}
		return d.WorkflowExecutionInfo.Execution.RunId, nil
	}
	var demoRun string
	for {
		demoRun, err = runID()
		if err != nil {
			return err
		}
		if demoRun != firstRun {
			break
		}
		if err = pause(ctx); err != nil {
			return err
		}
	}
	m.BeforeDuplicate, err = c.Query(ctx, id)
	if err != nil {
		return err
	}
	m.DuplicateReceipt, err = c.Submit(ctx, id, input, hello)
	if err != nil {
		return err
	}
	m.AfterDuplicate, err = c.Query(ctx, id)
	if err != nil {
		return err
	}
	current, err := runID()
	if err != nil {
		return err
	}
	if current != demoRun || m.FirstReceipt != m.DuplicateReceipt || m.AfterDuplicate.RunCounter != 1 || m.AfterDuplicate.TotalTurns != 1 || len(m.AfterDuplicate.Inbox) != 0 {
		return fmt.Errorf("duplicate changed state: %+v", m.AfterDuplicate)
	}
	if _, err = c.Submit(ctx, id, input, workflows.MessageInput{RequestID: "recorded-demo", Text: "/demo"}); err != nil {
		return err
	}
	seen := map[string]bool{}
	var successor string
	for {
		s, e := c.Query(ctx, id)
		if e != nil {
			return e
		}
		if s.LastRunFailure != nil {
			return fmt.Errorf("live run failed: %+v", s.LastRunFailure)
		}
		if ap := s.PendingApproval; ap != nil && !seen[ap.RequestID] {
			m.Approvals = append(m.Approvals, *ap)
			if e = c.Decide(ctx, id, workflows.DecisionInput{RequestID: ap.RequestID, ActionHash: ap.ActionHash, Approved: true, User: "replay-capture", Reason: "opt-in bundled sample capture"}); e != nil {
				return e
			}
			seen[ap.RequestID] = true
		}
		successor, e = runID()
		if e != nil {
			return e
		}
		if successor != demoRun {
			m.FinalState, e = c.Query(ctx, id)
			if e != nil {
				return e
			}
			break
		}
		if e = pause(ctx); e != nil {
			return e
		}
	}
	s := m.FinalState
	if s.RunCounter != 2 || s.TotalTurns != 6 || s.ChildCounter != 1 || s.ApprovalCounter != 3 || s.VerificationPending || s.LastRunFailure != nil || !strings.Contains(s.Answer, "Verification: go test ./... passed") || len(m.Approvals) != 3 || !strings.HasSuffix(m.Approvals[2].Call.ID, "/afterrun/tests") {
		return fmt.Errorf("unexpected final evidence: %+v approvals=%+v", s, m.Approvals)
	}
	fetch := func(wid, rid string) (*historypb.History, error) {
		h := &historypb.History{}
		it := sdk.GetWorkflowHistory(ctx, wid, rid, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for it.HasNext() {
			e, err := it.Next()
			if err != nil {
				return nil, err
			}
			h.Events = append(h.Events, e)
		}
		return h, nil
	}
	first, err := fetch(id, firstRun)
	if err != nil {
		return err
	}
	demo, err := fetch(id, demoRun)
	if err != nil {
		return err
	}
	var demoInput workflows.Input
	if err = converter.GetDefaultDataConverter().FromPayloads(demo.Events[0].GetWorkflowExecutionStartedEventAttributes().Input, &demoInput); err != nil {
		return err
	}
	if demoInput.Carry == nil || len(demoInput.Carry.Receipts) != 1 || demoInput.Carry.Receipts[0].RequestID != hello.RequestID || demoInput.Carry.Receipts[0].Receipt != m.FirstReceipt {
		return errors.New("missing actual retained receipt in CAN input")
	}
	next, err := fetch(id, successor)
	if err != nil {
		return err
	}
	var childID, childRun string
	for _, e := range demo.Events {
		if a := e.GetChildWorkflowExecutionStartedEventAttributes(); a != nil {
			childID = a.WorkflowExecution.WorkflowId
			childRun = a.WorkflowExecution.RunId
		}
	}
	if childID == "" || childRun == "" {
		return errors.New("no actual child start")
	}
	child, err := fetch(childID, childRun)
	if err != nil {
		return err
	}
	var childInput workflows.ExplorerInput
	if err = converter.GetDefaultDataConverter().FromPayloads(child.Events[0].GetWorkflowExecutionStartedEventAttributes().Input, &childInput); err != nil {
		return err
	}
	if childInput.Config.Carry != nil || childInput.ParentScope.WorkflowID != id || childInput.ParentScope.RunID != demoRun {
		return fmt.Errorf("unexpected child config: %+v", childInput)
	}
	histories := []*historypb.History{first, demo, next, child}
	ids := []string{id, id, id, childID}
	runs := []string{firstRun, demoRun, successor, childRun}
	names := []string{"hello-can.json", "demo-can.json", "successor.json", "explorer.json"}
	// Stage only after live checks. A replay failure is surfaced, never repaired.
	stage, err := os.MkdirTemp("", "replay-fixtures-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for i, h := range histories {
		raw, err := protojson.MarshalOptions{Indent: "  "}.Marshal(h)
		if err != nil {
			return err
		}
		sanitize(h.ProtoReflect(), root)
		b, err := protojson.MarshalOptions{Indent: "  "}.Marshal(h)
		if err != nil {
			return err
		}
		f := fixture{File: names[i], WorkflowID: ids[i], RunID: runs[i], WorkflowType: h.Events[0].GetWorkflowExecutionStartedEventAttributes().WorkflowType.Name, Events: len(h.Events), SHA256: fmt.Sprintf("%x", sha256.Sum256(b)), RawSHA256: fmt.Sprintf("%x", sha256.Sum256(raw))}
		m.Histories = append(m.Histories, f)
		if err = os.WriteFile(filepath.Join(stage, f.File), b, 0644); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = []byte(strings.ReplaceAll(string(b), root, "/replay-capture"))
	if err = os.WriteFile(filepath.Join(stage, "manifest.json"), b, 0644); err != nil {
		return err
	}
	if err = os.CopyFS(out, os.DirFS(stage)); err != nil {
		return err
	}
	// Keep genuine fixtures as the reproduction if replay ever exposes a bug.
	for i, h := range histories {
		f := m.Histories[i]
		r := worker.NewWorkflowReplayer()
		r.RegisterWorkflow(workflows.SessionWorkflow)
		r.RegisterWorkflow(workflows.ExplorerWorkflow)
		if err = r.ReplayWorkflowHistoryWithOptions(nil, h, worker.ReplayWorkflowHistoryOptions{OriginalExecution: workflowsExecution(f)}); err != nil {
			return fmt.Errorf("genuine history %s/%s failed replay; preserved for investigation, do not rewrite: %w", out, f.File, err)
		}
	}
	fmt.Printf("Captured and replayed %d genuine histories at %s (owned server %s, UI %d)\n", len(histories), out, address, ui)
	return nil
}

func workflowsExecution(f fixture) workflow.Execution {
	return workflow.Execution{ID: f.WorkflowID, RunID: f.RunID}
}

// Payload bytes are base64 in protojson: sanitize decoded json/plain, not the
// outer JSON. Unknown binary encodings and all non-path values remain untouched.
func sanitize(m protoreflect.Message, root string) {
	if p, ok := m.Interface().(*commonpb.Payload); ok {
		if string(p.Metadata["encoding"]) == "json/plain" {
			p.Data = []byte(strings.ReplaceAll(string(p.Data), root, "/replay-capture"))
		}
		return
	}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.IsMap() {
			return true
		}
		clean := func(v protoreflect.Value) protoreflect.Value {
			if fd.Kind() == protoreflect.MessageKind {
				sanitize(v.Message(), root)
			}
			if fd.Kind() == protoreflect.StringKind {
				return protoreflect.ValueOfString(strings.ReplaceAll(v.String(), root, "/replay-capture"))
			}
			return v
		}
		if fd.IsList() {
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				l.Set(i, clean(l.Get(i)))
			}
		} else {
			m.Set(fd, clean(v))
		}
		return true
	})
}
