//go:build integration

package webintegration_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
)

// Only the approval write boundary is faulted. SQL, retry exhaustion, Update
// adjudication, cancellation, sealing and final publication remain real.
type approvalCreationFault struct {
	*storage.Repository
	committed bool
	restored  atomic.Bool
	failures  atomic.Int32
}

func (r *approvalCreationFault) RecordWebApproval(ctx context.Context, p chat.WebApprovalRecord) error {
	if p.Outcome == "" && !r.restored.Load() {
		if r.committed {
			if err := r.Repository.RecordWebApproval(ctx, p); err != nil {
				return err
			}
		}
		r.failures.Add(1)
		return errors.New("injected approval creation unavailable/lost acknowledgment")
	}
	return r.Repository.RecordWebApproval(ctx, p)
}

type approvalCreationModel struct{}

func (approvalCreationModel) Chat(ctx context.Context, r llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	if r.Messages[len(r.Messages)-1].Content == "target approval creation" {
		return oneToolModel{}.Chat(ctx, r, emit)
	}
	return mock.New().Chat(ctx, r, emit)
}

func TestApprovalCreationRecoveryAcceptsControl(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "no_commit"
		if committed {
			name = "lost_ack_after_commit"
		}
		for _, kind := range []chat.CommandKind{chat.CommandStop, chat.CommandReplace} {
			t.Run(name+"/"+string(kind), func(t *testing.T) {
				f := setup(t)
				fault := &approvalCreationFault{Repository: f.repo, committed: committed}
				var effects atomic.Int32
				registry := tools.NewWebRegistry(map[string]tools.WebAdapter{"web_fetch": func(context.Context, chat.ApprovalScope, json.RawMessage) (chat.WebToolResult, error) {
					effects.Add(1)
					return chat.WebToolResult{Text: "unexpected execution"}, nil
				}})
				runner, err := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: fault, Model: approvalCreationModel{}, WebTools: registry})
				require.NoError(t, err)
				t.Cleanup(func() { runner.Close() })
				c := f.conversation(t, "qwen3.5:9b")
				target := f.command(t, c, "target approval creation")
				_, err = f.gateway.Submit(f.ctx, target)
				require.NoError(t, err)
				var state w.WebSessionState
				require.Eventually(t, func() bool {
					v, e := f.sdk.QueryWorkflow(f.ctx, c.WorkflowID, "", "state")
					return e == nil && v.Get(&state) == nil && state.Status == "recovering" && state.PendingApproval != nil
				}, 25*time.Second, 20*time.Millisecond, "creation must exhaust its actual activity retry batch")
				require.GreaterOrEqual(t, fault.failures.Load(), int32(5))
				pending := *state.PendingApproval
				var before int
				require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM approvals WHERE id=$1`, pending.ID).Scan(&before))
				if committed {
					require.Equal(t, 1, before)
				} else {
					require.Zero(t, before)
				}
				text := ""
				if kind == chat.CommandReplace {
					text = "replacement completes"
				}
				control := f.control(t, c, target.TurnID, kind, text)
				result, err := f.gateway.Submit(f.ctx, control)
				require.NoError(t, err)
				require.Equal(t, "accepted", result.State)
				fault.restored.Store(true)
				require.Eventually(t, func() bool {
					turn, e := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: target.TurnID})
					if e == nil && turn.Status == "cancelled" {
						return true
					}
					d, e := f.sdk.DescribeWorkflowExecution(f.ctx, c.WorkflowID, "")
					return e == nil && d.WorkflowExecutionInfo.Status == enumspb.WORKFLOW_EXECUTION_STATUS_FAILED
				}, 15*time.Second, 20*time.Millisecond)
				turn, err := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: target.TurnID})
				require.NoError(t, err)
				require.Equal(t, "cancelled", turn.Status, "accepted control must not terminate the session on an absent approval row")
				var outcome string
				var raw []byte
				var deadline time.Time
				require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT outcome,scope,deadline FROM approvals WHERE id=$1`, pending.ID).Scan(&outcome, &raw, &deadline))
				require.Equal(t, "cancelled", outcome)
				var scope chat.ApprovalScope
				require.NoError(t, json.Unmarshal(raw, &scope))
				require.Equal(t, pending.Scope, scope)
				require.True(t, pending.Deadline.Truncate(time.Microsecond).Equal(deadline))
				var sealed bool
				require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT model_invocations_closed FROM turns WHERE id=$1`, target.TurnID).Scan(&sealed))
				require.True(t, sealed)
				missing, err := f.repo.SealChatModelInvocations(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: target.TurnID})
				require.NoError(t, err)
				require.Zero(t, missing)
				if kind == chat.CommandReplace {
					f.completed(t, control)
				}
				follow := f.command(t, c, "follow-up completes")
				_, err = f.gateway.Submit(f.ctx, follow)
				require.NoError(t, err)
				f.completed(t, follow)
				require.Zero(t, effects.Load())
				var invocations int
				require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM web_tool_invocations WHERE conversation_id=$1`, c.ID).Scan(&invocations))
				require.Zero(t, invocations)
				retry, err := f.gateway.Submit(f.ctx, control)
				require.NoError(t, err)
				require.Equal(t, result, retry, "control receipt remains immutable")
			})
		}
	}
}
