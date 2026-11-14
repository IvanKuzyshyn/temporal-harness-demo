package workflows

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
)

const WebMarkCancellingActivity = "MarkChatCancelling"

type ReplacementInput = chat.ReplacementInput

// One replayable claim per active target. It survives receipt loss and CAN;
// replacement is held separately so even a full normal inbox cannot displace it.
type WebControl struct{ Command chat.CommandIdentity }

func (c *WebControl) receipt() chat.AdmissionResult {
	r := chat.AdmissionResult{State: "accepted", TargetTurnID: c.Command.ExpectedActiveTurnID}
	if c.Command.Kind == chat.CommandReplace {
		r.ReplacementTurnID = c.Command.TurnID
	}
	return r
}
func (s *WebSessionState) claimControl(c chat.CommandIdentity) chat.AdmissionResult {
	if s.Control != nil && s.Control.Command.CommandID == c.CommandID {
		return s.Control.receipt()
	}
	if s.Control != nil && s.Control.Command.ExpectedActiveTurnID == c.ExpectedActiveTurnID {
		return chat.AdmissionResult{State: "rejected", Code: "control_conflict"}
	}
	// Finalization (join/settle) is the completion linearization boundary.
	// Once the model returned, preserve that decision; SQL settlement may
	// already have committed when its acknowledgment is missing.
	if s.ActiveTurn == "" || s.ActiveTurn != c.ExpectedActiveTurnID || (s.RecoveryPhase == "join" || s.RecoveryPhase == "settle") {
		return chat.AdmissionResult{State: "rejected", Code: "stale_target"}
	}
	s.Control = &WebControl{Command: c}
	s.Status = "cancelling"
	return s.Control.receipt()
}
func validWebIdentity(c chat.CommandIdentity, conversation chat.ConversationID, model string) bool {
	kind := c.Kind == chat.CommandMessage || c.Kind == chat.CommandStop || c.Kind == chat.CommandReplace || c.Kind == chat.CommandDecide || c.Kind == chat.CommandRevoke
	return kind && c.ConversationID == conversation && c.Model == model && c.WorkflowID == "web-chat/"+string(conversation) && chat.ValidID(string(c.CommandID)) && chat.ValidID(c.RequestID) && chat.ValidHash(c.PayloadDigest) && c.Sequence > 0 && (c.TurnID == "" || chat.ValidID(string(c.TurnID))) && (c.ExpectedActiveTurnID == "" || chat.ValidID(string(c.ExpectedActiveTurnID)))
}

func validWebReceipt(r chat.AdmissionResult) bool {
	if r.State == "accepted" {
		return r.Code == "" && (r.TargetTurnID == "" || chat.ValidID(string(r.TargetTurnID))) && (r.ReplacementTurnID == "" || chat.ValidID(string(r.ReplacementTurnID)))
	}
	return r.State == "rejected" && (r.Code == "invalid_command" || r.Code == "stale_target" || r.Code == "control_conflict") && r.TargetTurnID == "" && r.ReplacementTurnID == ""
}
