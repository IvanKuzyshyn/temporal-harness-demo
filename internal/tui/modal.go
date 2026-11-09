package tui

// approvalView is the seam for the interactive approval modal (PR14). This
// placeholder deliberately never approves or rejects implicitly. The backend's
// Decide contract and the snapshot's pendingApproval are available to that modal.
func (m *Model) approvalView() string {
	p := m.state.PendingApproval
	if p == nil {
		return "Enter send · Alt+Enter steer · Shift+Enter newline"
	}
	return singleLine("Approval pending: " + p.RequestID + " · " + p.Call.Name + " · " + p.Preview + " (interactive decisions coming in PR14)")
}
