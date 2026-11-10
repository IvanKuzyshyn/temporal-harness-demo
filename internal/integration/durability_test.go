//go:build darwin || linux

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/converter"
)

func TestLiveDurability(t *testing.T) {
	s := newServer(t)
	// Deliberately sequential: task queue names are production constants, isolated
	// by this suite's private server, with exactly one live production worker.
	t.Run("ApprovalSurvivesWorkerAndClientRestart", s.approvalRestart)
	t.Run("WorkerLossDoesNotRetryUnsafeCommand", s.workerLoss)
	t.Run("StopJoinsCommandAndContinueRetainsQueue", s.stopContinue)
}

func inputFor(t *testing.T, scenario string) workflows.Input {
	t.Helper()
	root := t.TempDir()
	return workflows.Input{Model: "mock", Workdir: filepath.Join(root, "project"), DataDir: filepath.Join(root, "data"), Scenario: scenario, MaxTurns: 8, ApprovalTimeout: 3 * time.Minute, HangTimeout: 2 * time.Minute}
}

func pending(s workflows.State) bool {
	return s.PendingApproval != nil && s.PendingApproval.Decision == nil
}

func settled(s workflows.State) bool {
	return s.Status == "idle" && s.RunCounter > 0 && s.PendingApproval == nil && len(s.ActiveTools) == 0
}

func (s *liveServer) decide(t *testing.T, id string, p workflows.PendingApproval, approved bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	must(t, s.client.Decide(ctx, id, workflows.DecisionInput{RequestID: p.RequestID, ActionHash: p.ActionHash, Approved: approved, User: "live-integration", Reason: "explicit test decision"}))
}

func (s *liveServer) approvalRestart(t *testing.T) {
	const id = "durability-approval"
	in := inputFor(t, "write")
	// Never modify the checked-in deliberately broken sample.
	must(t, os.CopyFS(in.Workdir, os.DirFS("../../examples/sample-project")))
	path := filepath.Join(in.Workdir, "calc/calc.go")
	original := string(readFile(t, path))
	s.ownSession(t, id)
	w := s.startWorker(t)
	s.ownCommand(t, id, in, w) // Also own any after-run verification command.
	s.submit(t, id, in, "original-input", "Correct Add with human approval")
	before := s.state(t, id, pending)
	approval := *before.PendingApproval
	if approval.Call.Name != "write_file" || approval.RequestID == "" || approval.ActionHash == "" {
		t.Fatalf("invalid pending write: %+v", approval)
	}
	if out := s.cliWait(t, id, "blocked"); !strings.Contains(out, approval.RequestID) {
		t.Fatalf("CLI did not recover pending request: %s", out)
	}
	if got := string(readFile(t, path)); got != original {
		t.Fatal("file changed without approval")
	}
	w.stop(t, syscall.SIGTERM)
	if w.err != nil {
		t.Fatalf("graceful worker shutdown: %v", w.err)
	}
	// Drop the display history while no worker can write it. Query/transcript,
	// not the lossy JSONL, must recover the original pending request.
	display, err := events.NewStore(in.DataDir).Path(id)
	must(t, err)
	must(t, os.Remove(display))
	s.reconnect(t)
	s.startWorker(t)
	after := s.state(t, id, pending)
	if !reflect.DeepEqual(after.PendingApproval, &approval) || after.TranscriptRef != before.TranscriptRef {
		t.Fatalf("approval changed on replay: before=%+v after=%+v", approval, after.PendingApproval)
	}
	if got := string(readFile(t, path)); got != original {
		t.Fatal("file changed during worker restart")
	}
	if out := s.cliWait(t, id, "blocked"); !strings.Contains(out, approval.RequestID) {
		t.Fatalf("restarted CLI lost pending request: %s", out)
	}
	for _, invalid := range []struct {
		decision workflows.DecisionInput
		want     string
	}{
		{workflows.DecisionInput{RequestID: "stale-request", ActionHash: approval.ActionHash, Approved: true}, "unknown or stale approval request"},
		{workflows.DecisionInput{RequestID: approval.RequestID, ActionHash: "mismatched-hash", Approved: true}, "approval action hash mismatch"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := s.client.Decide(ctx, id, invalid.decision)
		cancel()
		if err == nil || !strings.Contains(err.Error(), invalid.want) {
			t.Fatalf("invalid decision rejection: got %v, want %s", err, invalid.want)
		}
		t.Logf("expected decision rejection: %v", err)
	}
	still := s.state(t, id, pending)
	if !reflect.DeepEqual(still.PendingApproval, &approval) || string(readFile(t, path)) != original {
		t.Fatal("invalid decisions changed approval/file")
	}
	s.decide(t, id, approval, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = s.client.Decide(ctx, id, workflows.DecisionInput{RequestID: approval.RequestID, ActionHash: approval.ActionHash, Approved: true})
	cancel()
	if err == nil || (!strings.Contains(err.Error(), "already recorded") && !strings.Contains(err.Error(), "unknown or stale")) {
		t.Fatalf("duplicate decision rejection missing: %v", err)
	}
	t.Logf("expected duplicate rejection: %v", err)
	// Production after-run must request a separate approval for verification.
	verification := s.state(t, id, func(state workflows.State) bool {
		return pending(state) && state.PendingApproval.Call.Name == "run_command"
	})
	if verification.PendingApproval.RequestID == approval.RequestID || verification.PendingApproval.ActionHash == approval.ActionHash {
		t.Fatal("write approval improperly reused for verification")
	}
	if got := string(readFile(t, path)); got != "package calc\n\nfunc Add(a, b int) int { return a + b }\n" {
		t.Fatalf("approved write missing: %s", got)
	}
	s.decide(t, id, *verification.PendingApproval, true)
	final := s.state(t, id, settled)
	if final.LastRunFailure != nil || final.Answer == "" || final.VerificationPending {
		t.Fatalf("write/real go test verification failed: %+v", final)
	}
	assertTranscript(t, in, final, "", false)
	s.reconnect(t)
	if out := s.cliWait(t, id, "settled"); !strings.Contains(out, final.Answer) {
		t.Fatalf("fresh CLI did not recover authoritative answer: %s", out)
	}
	t.Log("real worker/client/CLI restart preserved exact approval; stale/hash/duplicate rejected; sample fixed only after approval and verified by real go test ./...")
}

// The generated fixture is approved Go project code, not a shell escape. The
// quick package produces a real go test log line even while the second package
// is blocked (go test buffers an individual package's test stdout).
func commandFixture(t *testing.T, in workflows.Input) {
	t.Helper()
	writeFile(t, filepath.Join(in.Workdir, "go.mod"), "module durabilityfixture\n\ngo 1.24\n")
	writeFile(t, filepath.Join(in.Workdir, "aaa/quick_test.go"), "package aaa\nimport \"testing\"\nfunc TestQuick(t *testing.T) {}\n")
	writeFile(t, filepath.Join(in.Workdir, "zzz/blocked_test.go"), `package zzz
import (
 "encoding/json"
 "os"
 "os/exec"
 "path/filepath"
 "syscall"
 "testing"
 "time"
)
func TestBlocked(t *testing.T) {
 root := ".."
 if os.Getenv("DURABILITY_FIXTURE_CHILD") == "1" {
  if err := os.WriteFile(filepath.Join(root, "child.ready"), []byte("ready"), 0600); err != nil { t.Fatal(err) }
  for { time.Sleep(time.Second) }
 }
 // O_APPEND records every execution, even a forbidden rerun. O_EXCL creates
 // a positive first-start marker; a second invocation must fail visibly.
 f, err := os.OpenFile(filepath.Join(root, "executions"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
 if err != nil { t.Fatal(err) }
 if _, err = f.WriteString("started\n"); err != nil { t.Fatal(err) }; f.Sync(); f.Close()
 f, err = os.OpenFile(filepath.Join(root, "started"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
 if err != nil { t.Fatal(err) }; f.Close()
 bin, err := os.Executable(); if err != nil { t.Fatal(err) }
 child := exec.Command(bin, "-test.run=^TestBlocked$")
 child.Env = append(os.Environ(), "DURABILITY_FIXTURE_CHILD=1")
 if err = child.Start(); err != nil { t.Fatal(err) }
 defer func() { child.Process.Kill(); child.Wait() }()
 group, err := syscall.Getpgid(0); if err != nil { t.Fatal(err) }
 b, _ := json.Marshal(map[string]int{"group":group,"test":os.Getpid(),"child":child.Process.Pid})
 tmp := filepath.Join(root,"pids.tmp")
 if err = os.WriteFile(tmp,b,0600); err != nil { t.Fatal(err) }
 if err = os.Rename(tmp,filepath.Join(root,"pids.json")); err != nil { t.Fatal(err) }
 for { time.Sleep(time.Second) }
}
`)
}

type commandPIDs struct {
	Group int `json:"group"`
	Test  int `json:"test"`
	Child int `json:"child"`
}

type heartbeat struct {
	PID int `json:"pid"`
}

// Cleanup is separate from the assertion: SIGKILL of a worker does NOT certify
// termination of the command's separate process group. We capture it from the
// actual server heartbeat and the fixture, then explicitly clean that group.
func (s *liveServer) ownCommand(t *testing.T, id string, in workflows.Input, w *ownedProcess) *commandPIDs {
	t.Helper()
	owned := new(commandPIDs)
	// Register BEFORE approval: an accepted Update may outlive a client timeout.
	t.Cleanup(func() {
		// Prevent new command starts before recovering any final ownership data.
		w.stop(t, syscall.SIGTERM)
		if b, err := os.ReadFile(filepath.Join(in.Workdir, "pids.json")); err == nil {
			if err := json.Unmarshal(b, owned); err != nil {
				t.Errorf("read owned fixture PIDs: %v", err)
			}
		}
		if owned.Group == 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if desc, err := s.sdk.DescribeWorkflowExecution(ctx, id, ""); err == nil {
				for _, a := range desc.PendingActivities {
					if a.ActivityType.GetName() == workflows.CommandActivity && a.HeartbeatDetails != nil {
						var hb heartbeat
						if converter.GetDefaultDataConverter().FromPayloads(a.HeartbeatDetails, &hb) == nil {
							owned.Group = hb.PID
						}
					}
				}
			}
		}
		if owned.Group > 1 {
			_ = syscall.Kill(-owned.Group, syscall.SIGKILL)
			assertDead(t, *owned)
		}
	})
	return owned
}

func (s *liveServer) observeCommand(t *testing.T, id string, in workflows.Input, owned *commandPIDs) commandPIDs {
	t.Helper()
	var pids commandPIDs
	var heartbeatPID int
	eventually(t, 35*time.Second, "command heartbeat, intent, positive start and descendant", func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		desc, err := s.sdk.DescribeWorkflowExecution(ctx, id, "")
		if err != nil {
			return false, err.Error()
		}
		for _, a := range desc.PendingActivities {
			if a.ActivityType.GetName() != workflows.CommandActivity || a.HeartbeatDetails == nil {
				continue
			}
			var hb heartbeat
			if err := converter.GetDefaultDataConverter().FromPayloads(a.HeartbeatDetails, &hb); err != nil {
				return false, err.Error()
			}
			heartbeatPID = hb.PID
			owned.Group = hb.PID
			if a.Attempt != 1 || heartbeatPID <= 1 {
				t.Fatalf("unexpected command attempt/heartbeat: %+v", a)
			}
		}
		b, err := os.ReadFile(filepath.Join(in.Workdir, "pids.json"))
		if err != nil {
			return false, err.Error()
		}
		if err := json.Unmarshal(b, &pids); err != nil {
			return false, err.Error()
		}
		*owned = pids
		_, childErr := os.Stat(filepath.Join(in.Workdir, "child.ready"))
		logs, err := filepath.Glob(filepath.Join(in.DataDir, "artifacts", "*", "cmd-*.log"))
		if err != nil || len(logs) != 1 {
			return false, fmt.Sprintf("intent logs=%v err=%v", logs, err)
		}
		tail, err := os.ReadFile(logs[0])
		return err == nil && strings.Contains(string(tail), "durabilityfixture/aaa") && childErr == nil && pids.Group == heartbeatPID && pids.Test > 1 && pids.Child > 1, fmt.Sprintf("pids=%+v heartbeat=%d child=%v tail=%q", pids, heartbeatPID, childErr, tail)
	})
	assertOneExecution(t, in)
	for _, pid := range []int{pids.Group, pids.Test, pids.Child} {
		if !processAlive(pid) {
			t.Fatalf("fixture process %d not live before intervention", pid)
		}
	}
	t.Logf("real command activity attempt=1 heartbeatPID=%d positive start/execution=1; command/test/descendant=%+v", heartbeatPID, pids)
	return pids
}

func (s *liveServer) beginCommand(t *testing.T, id string) (workflows.Input, *ownedProcess, commandPIDs) {
	t.Helper()
	s.reconnect(t)
	in := inputFor(t, "command")
	commandFixture(t, in)
	s.ownSession(t, id)
	w := s.startWorker(t)
	owned := s.ownCommand(t, id, in, w)
	s.submit(t, id, in, "command-input", "Run the approved generated Go tests")
	state := s.state(t, id, pending)
	if state.PendingApproval.Call.Name != "run_command" || string(state.PendingApproval.Call.Args) != `{"args":["test","./..."],"cmd":"go"}` {
		t.Fatalf("unexpected command proposal: %+v", state.PendingApproval)
	}
	s.decide(t, id, *state.PendingApproval, true)
	pids := s.observeCommand(t, id, in, owned)
	return in, w, pids
}

func (s *liveServer) workerLoss(t *testing.T) {
	const id = "durability-worker-loss"
	in, w, pids := s.beginCommand(t, id)
	w.stop(t, syscall.SIGKILL)
	if w.err == nil {
		t.Fatal("expected killed worker exit status")
	}
	orphanAlive := processAlive(pids.Child)
	s.reconnect(t)
	s.startWorker(t)
	final := s.state(t, id, func(state workflows.State) bool { return settled(state) && state.LastRunFailure != nil })
	if final.LastRunFailure.Reason != "interrupted" || !final.QueuePaused {
		t.Fatalf("unsafe worker loss did not fail closed: %+v", final)
	}
	assertOneExecution(t, in)
	assertTranscript(t, in, final, "mock-0-0", true)
	s.assertCommandHistory(t, id, enumspb.EVENT_TYPE_ACTIVITY_TASK_TIMED_OUT)
	// The orphan still running is expected; only live Stop guarantees cancellation.
	t.Logf("hard worker loss: descendant alive immediately after SIGKILL=%v, after recovery=%v; explicitly killing owned command group %d now (not credited to restart)", orphanAlive, processAlive(pids.Child), pids.Group)
	_ = syscall.Kill(-pids.Group, syscall.SIGKILL)
	assertDead(t, pids)
	s.reconnect(t)
	recovered := s.state(t, id, settled)
	assertTranscript(t, in, recovered, "mock-0-0", true)
}

func (s *liveServer) stopContinue(t *testing.T) {
	const id = "durability-stop"
	in, _, pids := s.beginCommand(t, id)
	s.submit(t, id, in, "queued-followup", "Summarize the interrupted command without rerunning it")
	s.state(t, id, func(state workflows.State) bool { return len(state.Inbox) == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	must(t, s.client.Stop(ctx, id))
	cancel()
	final := s.state(t, id, func(state workflows.State) bool { return settled(state) && state.LastRunFailure != nil })
	if final.LastRunFailure.Reason != "aborted" || !final.QueuePaused || len(final.Inbox) != 1 {
		t.Fatalf("Stop lost or ran retained queue: %+v", final)
	}
	// Assert death BEFORE our explicit cleanup runs. A mere Stop receipt is not
	// evidence of joined activity cancellation; settlement and history are too.
	assertDead(t, pids)
	assertTranscript(t, in, final, "mock-0-0", true)
	s.assertCommandHistory(t, id, enumspb.EVENT_TYPE_ACTIVITY_TASK_CANCELED)
	// Query repeatedly to prove the queue stays paused without a Continue Update.
	for i := 0; i < 5; i++ {
		state := s.state(t, id, settled)
		if !state.QueuePaused || len(state.Inbox) != 1 || state.RunCounter != final.RunCounter {
			t.Fatalf("queued work resumed implicitly: %+v", state)
		}
		time.Sleep(100 * time.Millisecond)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	must(t, s.client.Continue(ctx, id))
	cancel()
	resumed := s.state(t, id, func(state workflows.State) bool {
		return settled(state) && state.RunCounter > final.RunCounter && len(state.Inbox) == 0 && !state.QueuePaused
	})
	if resumed.LastRunFailure != nil || resumed.HasUnansweredContext || resumed.Answer == "" {
		t.Fatalf("Continue did not settle retained context: %+v", resumed)
	}
	assertOneExecution(t, in)
	assertTranscript(t, in, resumed, "mock-0-0", true)
	s.assertCommandHistory(t, id, enumspb.EVENT_TYPE_ACTIVITY_TASK_CANCELED)
	t.Log("live Stop joined real go test process group/descendant; FIFO paused; explicit Continue answered matched transcript and retained follow-up without rerunning unsafe command")
}

func assertOneExecution(t *testing.T, in workflows.Input) {
	t.Helper()
	if got := string(readFile(t, filepath.Join(in.Workdir, "executions"))); got != "started\n" {
		t.Fatalf("command executed more/less than once: %q", got)
	}
	if _, err := os.Stat(filepath.Join(in.Workdir, "started")); err != nil {
		t.Fatalf("positive atomic start marker missing: %v", err)
	}
	logs, err := filepath.Glob(filepath.Join(in.DataDir, "artifacts", "*", "cmd-*.log"))
	must(t, err)
	if len(logs) != 1 {
		t.Fatalf("expected one exclusive command intent log, got %v", logs)
	}
}

func assertTranscript(t *testing.T, in workflows.Input, state workflows.State, interruptedID string, wantInterrupted bool) {
	t.Helper()
	messages, err := activities.ReadTranscript(in.DataDir, state.TranscriptRef)
	must(t, err)
	if len(messages) == 0 {
		t.Fatal("missing persisted transcript")
	}
	unmatched := map[string]bool{}
	interrupted := 0
	for _, m := range messages {
		if (m.Role == "assistant" || m.Role == "user") && len(unmatched) > 0 {
			t.Fatalf("new %s message exposed unmatched calls: %v", m.Role, unmatched)
		}
		for _, c := range m.ToolCalls {
			if unmatched[c.ID] {
				t.Fatalf("duplicate open tool call %s", c.ID)
			}
			unmatched[c.ID] = true
		}
		if m.Role == "tool" {
			if !unmatched[m.ToolCallID] {
				t.Fatalf("unmatched/duplicate tool result %s", m.ToolCallID)
			}
			delete(unmatched, m.ToolCallID)
			if m.ToolCallID == interruptedID {
				var result tools.Result
				must(t, json.Unmarshal([]byte(m.Content), &result))
				if !result.Interrupted || !result.IsError || !strings.Contains(result.Output, "durabilityfixture/aaa") || result.Details["logRef"] == "" {
					t.Fatalf("lost interrupted command outcome/tail: %+v", result)
				}
				interrupted++
			}
		}
	}
	if len(unmatched) != 0 || (wantInterrupted && interrupted != 1) {
		t.Fatalf("unmatched=%v persisted interrupted results=%d", unmatched, interrupted)
	}
}

func (s *liveServer) assertCommandHistory(t *testing.T, id string, terminal enumspb.EventType) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	it := s.sdk.GetWorkflowHistory(ctx, id, "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	var scheduledID int64
	var scheduled, started, ended int
	for it.HasNext() {
		e, err := it.Next()
		must(t, err)
		if a := e.GetActivityTaskScheduledEventAttributes(); a != nil && a.ActivityType.Name == workflows.CommandActivity {
			scheduled++
			scheduledID = e.EventId
			if a.RetryPolicy.GetMaximumAttempts() != 1 || a.HeartbeatTimeout.AsDuration() != 10*time.Second {
				t.Fatalf("unsafe command options changed: %+v", a)
			}
		}
		if a := e.GetActivityTaskStartedEventAttributes(); a != nil && a.ScheduledEventId == scheduledID && scheduledID != 0 {
			started++
			if a.Attempt != 1 {
				t.Fatalf("unsafe activity retried: attempt=%d", a.Attempt)
			}
		}
		if e.EventType == terminal && scheduledID != 0 {
			switch terminal {
			case enumspb.EVENT_TYPE_ACTIVITY_TASK_TIMED_OUT:
				a := e.GetActivityTaskTimedOutEventAttributes()
				if a.ScheduledEventId == scheduledID {
					ended++
					if a.Failure.GetTimeoutFailureInfo().GetTimeoutType() != enumspb.TIMEOUT_TYPE_HEARTBEAT {
						t.Fatalf("expected real heartbeat timeout: %+v", a)
					}
				}
			case enumspb.EVENT_TYPE_ACTIVITY_TASK_CANCELED:
				if e.GetActivityTaskCanceledEventAttributes().ScheduledEventId == scheduledID {
					ended++
				}
			}
		}
	}
	if scheduled != 1 || started != 1 || ended != 1 {
		t.Fatalf("command history scheduled=%d started=%d %s=%d", scheduled, started, terminal, ended)
	}
}

func processAlive(pid int) bool {
	if pid <= 1 || syscall.Kill(pid, 0) != nil {
		return false
	}
	// An orphan can remain as a zombie until init reaps it; it cannot execute.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		// A failed/timeout ps is not evidence of death; only ESRCH is.
		return syscall.Kill(pid, 0) != syscall.ESRCH
	}
	return strings.TrimSpace(string(b)) != "" && !strings.HasPrefix(strings.TrimSpace(string(b)), "Z")
}

func assertDead(t *testing.T, pids commandPIDs) {
	t.Helper()
	eventually(t, 5*time.Second, "owned command/test/descendant death", func() (bool, string) {
		for _, pid := range []int{pids.Group, pids.Test, pids.Child} {
			if processAlive(pid) {
				return false, fmt.Sprintf("PID %d remains live", pid)
			}
		}
		return true, ""
	})
}
