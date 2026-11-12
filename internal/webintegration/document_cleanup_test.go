//go:build integration

package webintegration_test

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	tw "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

type documentTimeouts struct {
	interceptor.WorkerInterceptorBase
}
type documentInbound struct {
	interceptor.WorkflowInboundInterceptorBase
}
type documentOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
}

func (*documentTimeouts) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &documentInbound{interceptor.WorkflowInboundInterceptorBase{Next: next}}
}
func (i *documentInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&documentOutbound{interceptor.WorkflowOutboundInterceptorBase{Next: next}})
}
func (i *documentOutbound) ExecuteActivity(ctx workflow.Context, name string, args ...interface{}) workflow.Future {
	if name == "AnalyzeDocumentChunk" || name == "AnalyzeAttachmentImage" {
		opts := workflow.GetActivityOptions(ctx)
		opts.StartToCloseTimeout = 3 * time.Second
		opts.HeartbeatTimeout = time.Second
		retry := *opts.RetryPolicy
		retry.InitialInterval = 100 * time.Millisecond
		opts.RetryPolicy = &retry
		ctx = workflow.WithActivityOptions(ctx, opts)
	}
	return i.Next.ExecuteActivity(ctx, name, args...)
}

type unjoinedDocumentModel struct {
	first, second, release, returned chan struct{}
	once1, once2                     sync.Once
}

func (m *unjoinedDocumentModel) Chat(ctx context.Context, r llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	if len(r.Tools) > 0 {
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "replacement completed"}}, nil
	}
	if activity.GetInfo(ctx).Attempt == 1 {
		m.once1.Do(func() { close(m.first) })
		<-ctx.Done()
		<-m.release
		close(m.returned)
		return llm.Response{}, ctx.Err()
	}
	m.once2.Do(func() { close(m.second) })
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}
func TestParentCancellationJoinsDocumentChild(t *testing.T)    { testAnalysisPhysicalJoin(t, false) }
func TestImageChildCancellationJoinsPhysicalBody(t *testing.T) { testAnalysisPhysicalJoin(t, true) }
func testAnalysisPhysicalJoin(t *testing.T, imageInput bool) {
	f := setup(t)
	store, e := attachments.NewStore(t.TempDir(), f.repo, nil)
	require.NoError(t, e)
	defer store.Close()
	m := &unjoinedDocumentModel{first: make(chan struct{}), second: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	release := sync.OnceFunc(func() { close(m.release) })
	a := &activities.ChatActivities{Repository: f.repo, Model: m, Attachments: store}
	var workers []tw.Worker
	for _, q := range []string{w.WebAgentsQueue, w.WebLLMQueue, w.WebDocumentsQueue} {
		worker := tw.New(f.sdk, q, tw.Options{Interceptors: []interceptor.WorkerInterceptor{&documentTimeouts{}}, MaxHeartbeatThrottleInterval: 100 * time.Millisecond, DefaultHeartbeatThrottleInterval: 100 * time.Millisecond})
		switch q {
		case w.WebAgentsQueue:
			worker.RegisterWorkflow(w.WebChatSessionWorkflow)
			worker.RegisterActivity(a)
		case w.WebDocumentsQueue:
			worker.RegisterWorkflow(w.DocumentAnalysisWorkflow)
			worker.RegisterActivity(a.PrepareDocumentAnalysis)
			worker.RegisterActivity(a.AnalyzeDocumentChunk)
			worker.RegisterActivity(a.AnalyzeAttachmentImage)
			worker.RegisterActivity(a.FinishDocumentAnalysis)
		case w.WebLLMQueue:
			worker.RegisterActivityWithOptions(a.ChatStep, activity.RegisterOptions{Name: w.WebModelActivity})
		}
		require.NoError(t, worker.Start())
		workers = append(workers, worker)
	}
	defer func() {
		release()
		for i := len(workers) - 1; i >= 0; i-- {
			workers[i].Stop()
		}
	}()
	c := f.conversation(t, "qwen3.5:9b")
	data := []byte("one small chunk")
	metadata := attachments.UploadMetadata{Name: "held.txt", MediaType: "text/plain"}
	if imageInput {
		data, e = os.ReadFile("../../scripts/testdata/bonjour.png")
		require.NoError(t, e)
		metadata = attachments.UploadMetadata{Name: "held.png", MediaType: "image/png"}
	}
	file, e := store.Put(f.ctx, c.ID, bytes.NewReader(data), metadata)
	require.NoError(t, e)
	cmd := f.attachmentCommand(t, c, file.ID)
	_, e = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, e)
	p := f.pending(t, c)
	_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, p, chat.ApprovalOnce))
	require.NoError(t, e)
	waitBarrier(t, m.first)
	waitBarrier(t, m.second)
	replacement := f.control(t, c, cmd.TurnID, chat.CommandReplace, "new request after physical join")
	accepted, e := f.gateway.Submit(f.ctx, replacement)
	require.NoError(t, e)
	require.Equal(t, "accepted", accepted.State)
	require.Eventually(t, func() bool { return f.approvalState(t, c).Status == "cancelling_interrupted" }, 12*time.Second, 50*time.Millisecond)
	in := chat.DocumentAnalysisInput{Scope: p.Scope, Model: c.Model, Budget: chat.DefaultDocumentBudget()}
	info, e := f.sdk.DescribeWorkflowExecution(f.ctx, in.ChildID(), "")
	require.NoError(t, e)
	require.Equal(t, enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, info.WorkflowExecutionInfo.Status)
	var missing int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM web_tool_invocations WHERE turn_id=$1 AND cleanup_confirmed_at IS NULL`, cmd.TurnID).Scan(&missing))
	require.Equal(t, 1, missing, "logical child cancellation cannot ACK the timed-out physical body")
	target, e := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID})
	require.NoError(t, e)
	require.Equal(t, "cancelling", target.Status)
	next, e := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: replacement.TurnID})
	require.NoError(t, e)
	require.Equal(t, "queued", next.Status)
	release()
	waitBarrier(t, m.returned)
	f.completed(t, replacement)
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM web_tool_invocations WHERE turn_id=$1 AND cleanup_confirmed_at IS NULL`, cmd.TurnID).Scan(&missing))
	require.Zero(t, missing)
	t.Log("actual child cancelled; timed-out first body blocked replacement until normal return+SQL ACK; logical retry and every physical body joined")
}
