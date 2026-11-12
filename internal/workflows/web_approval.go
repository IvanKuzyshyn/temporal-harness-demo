package workflows

import (
	"strconv"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	WebApprovalActivity      = "RecordWebApproval"
	WebAuthorizationActivity = "RecordWebAuthorization"
	WebToolActivity          = "ExecuteWebTool"
)

func registeredWebScope(s chat.ApprovalScope) bool {
	return s.ToolsetVersion == chat.WebToolset && s.ToolVersion == "1" && (s.ToolName == "web_fetch" || s.ToolName == "file_upload") && chat.ValidHash(s.ActionHash) && chat.ValidHash(s.ContentRef) && chat.ValidID(s.ToolCallID) && s.PolicyRevision >= 0
}
func (s *WebSessionState) decideWeb(c chat.CommandIdentity, now time.Time) chat.AdmissionResult {
	reject := chat.AdmissionResult{State: "rejected", Code: "invalid_approval"}
	if c.Kind == chat.CommandRevoke {
		s.Policy.Revision++
		s.Policy.GrantedToolset = ""
		// Pending scope must be renewed under the new revision, not retroactively
		// authorized by a stale decision. Already-authorized calls are unaffected.
		return chat.AdmissionResult{State: "accepted", PolicyRevision: s.Policy.Revision}
	}
	p := s.PendingApproval
	if p == nil || s.Control != nil || s.ActiveTurn != p.Scope.TurnID || p.ID != c.ApprovalID || p.Scope.ActionHash != c.ActionHash || p.Decision != "" {
		return reject
	}
	if !now.Before(p.Deadline) {
		reject.Code = "approval_expired"
		return reject
	}
	if p.Scope.PolicyRevision != s.Policy.Revision {
		reject.Code = "policy_conflict"
		return reject
	}
	if !registeredWebScope(p.Scope) {
		return reject
	}
	if c.Decision != chat.ApprovalOnce && c.Decision != chat.ApprovalAll && c.Decision != chat.ApprovalDeny {
		return reject
	}
	p.Decision = c.Decision
	if c.Decision == chat.ApprovalAll {
		s.Policy.Revision++
		s.Policy.GrantedToolset = chat.WebToolset
	}
	return chat.AdmissionResult{State: "accepted", PolicyRevision: s.Policy.Revision, GrantedToolset: s.Policy.GrantedToolset}
}

// webBatch records each authorization in deterministic order before scheduling
// any of the (at most four) calls. Only references cross the workflow boundary.
// It yields to the outer admission/CAN loop with no live activity when requested.
func (s *WebSessionState) webBatch(ctx, turnCtx, join workflow.Context, model string, pause func() bool) error {
	for i := range s.Batch {
		a := &s.Batch[i]
		if !registeredWebScope(a.Scope) || a.Scope.ConversationID != s.ConversationID || a.Scope.TurnID != s.ActiveTurn {
			return temporal.NewNonRetryableApplicationError("invalid web action", "InvalidRequest", nil)
		}
		if a.Recorded {
			continue
		}
		if s.Control != nil {
			return nil
		}
		if pause() {
			return nil
		}
		// A failed resolution write leaves the decision on THIS action. Finish
		// that exact request before moving on, including after CAN.
		if p := s.PendingApproval; p != nil && a.Outcome != "" {
			if p.Scope != a.Scope {
				return temporal.NewNonRetryableApplicationError("unbound pending approval", "InvalidRequest", nil)
			}
			if err := workflow.ExecuteActivity(webStorageContext(ctx), WebApprovalActivity, chat.WebApprovalRecord{Approval: *p, Outcome: a.Outcome}).Get(ctx, nil); err != nil {
				return err
			}
			s.PendingApproval = nil
		}
		if a.Outcome == "" {
			if s.PendingApproval == nil && s.Policy.GrantedToolset == chat.WebToolset {
				a.Scope.PolicyRevision = s.Policy.Revision
				a.Outcome = "authorized"
			} else {
				if s.PendingApproval == nil {
					a.Scope.PolicyRevision = s.Policy.Revision
					s.PendingApproval = &chat.WebApproval{ID: chat.LogicalID(s.ActiveTurn, "approval/"+a.Scope.ToolCallID+"/"+strconv.FormatInt(s.Policy.Revision, 10)), Scope: a.Scope, Deadline: workflow.Now(ctx).Add(24 * time.Hour)}
				}
				p := s.PendingApproval
				if p.Scope != a.Scope {
					return temporal.NewNonRetryableApplicationError("unbound pending approval", "InvalidRequest", nil)
				}
				s.Status = "awaiting_approval"
				if err := workflow.ExecuteActivity(webStorageContext(ctx), WebApprovalActivity, chat.WebApprovalRecord{Approval: *p}).Get(ctx, nil); err != nil {
					return err
				}
				_, err := workflow.AwaitWithTimeout(turnCtx, p.Deadline.Sub(workflow.Now(ctx)), func() bool { return p.Decision != "" || p.Scope.PolicyRevision != s.Policy.Revision || pause() })
				if s.Control != nil {
					return nil
				}
				if err != nil {
					return err
				}
				if pause() {
					return nil
				}
				// A decision mutates workflow state before its immutable receipt is stored.
				// Never execute until its handler has durably finished or checkpointed.
				if err = workflow.Await(turnCtx, func() bool { return workflow.AllHandlersFinished(ctx) || pause() }); err != nil {
					if s.Control != nil {
						return nil
					}
					return err
				}
				if pause() {
					return nil
				}
				switch {
				case p.Decision == chat.ApprovalOnce || p.Decision == chat.ApprovalAll:
					a.Outcome = "authorized"
				case p.Decision == chat.ApprovalDeny:
					a.Outcome = "denied"
				case p.Scope.PolicyRevision != s.Policy.Revision:
					if err = workflow.ExecuteActivity(webStorageContext(ctx), WebApprovalActivity, chat.WebApprovalRecord{Approval: *p, Outcome: "revoked"}).Get(ctx, nil); err != nil {
						return err
					}
					s.PendingApproval = nil
					return nil
				default:
					a.Outcome = "expired"
				}
				if err = workflow.ExecuteActivity(webStorageContext(ctx), WebApprovalActivity, chat.WebApprovalRecord{Approval: *p, Outcome: a.Outcome}).Get(ctx, nil); err != nil {
					return err
				}
				s.PendingApproval = nil
			}
		}
		if err := workflow.ExecuteActivity(webStorageContext(ctx), WebAuthorizationActivity, *a).Get(ctx, nil); err != nil {
			return err
		}
		a.Recorded = true
	}
	if s.Control != nil || pause() {
		return nil
	}
	futures := make([]workflow.Future, 0, len(s.Batch))
	opts := workflow.ActivityOptions{TaskQueue: WebAgentsQueue, StartToCloseTimeout: time.Minute, ScheduleToCloseTimeout: 3 * time.Minute, HeartbeatTimeout: 15 * time.Second, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 2, InitialInterval: time.Second}}
	for _, a := range s.Batch {
		if a.Outcome == "authorized" {
			if a.Scope.ToolName == "file_upload" {
				in := chat.DocumentAnalysisInput{Scope: a.Scope, Model: model, Budget: chat.DefaultDocumentBudget()}
				child := workflow.WithChildOptions(turnCtx, workflow.ChildWorkflowOptions{WorkflowID: in.ChildID(), TaskQueue: WebDocumentsQueue, WorkflowExecutionTimeout: 15 * time.Minute, WaitForCancellation: true, ParentClosePolicy: enumspb.PARENT_CLOSE_POLICY_REQUEST_CANCEL, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE})
				futures = append(futures, workflow.ExecuteChildWorkflow(child, DocumentAnalysisName, in))
			} else {
				futures = append(futures, workflow.ExecuteActivity(workflow.WithActivityOptions(turnCtx, opts), WebToolActivity, chat.WebToolExecution{Scope: a.Scope}))
			}
		}
	}
	var first error
	for _, f := range futures {
		if err := f.Get(join, nil); err != nil && first == nil {
			first = err
		}
	}
	if s.Control != nil {
		return nil
	}
	if first != nil {
		// Retry policy for a logical execution is bounded. Do not silently
		// create a fresh activity with reset attempt numbers after exhaustion.
		return temporal.NewNonRetryableApplicationError("web tool execution exhausted", "WebToolFailure", first)
	}
	s.Batch = nil
	s.Step++
	s.RecoveryPhase = "model"
	s.Status = "running"
	return nil
}
