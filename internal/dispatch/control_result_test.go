package dispatch

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestControlReceiptBounds(t *testing.T) {
	for _, r := range []CommandResult{
		{State: "accepted", PolicyRevision: -1},
		{State: "accepted", GrantedToolset: strings.Repeat("x", 129)},
		{State: "rejected", Code: "invalid_approval", PolicyRevision: 1},
		{State: "accepted", TargetTurnID: "unbounded-or-invalid"},
		{State: "accepted", ReplacementTurnID: "00000000-0000-4000-8000-000000000001"},
		{State: "rejected", Code: "control_conflict", TargetTurnID: "00000000-0000-4000-8000-000000000001"},
	} {
		require.False(t, validResult(r), "malformed receipt must remain retryable: %#v", r)
	}
	require.True(t, validResult(chat.AdmissionResult{State: "accepted", TargetTurnID: "00000000-0000-4000-8000-000000000001", ReplacementTurnID: "00000000-0000-4000-8000-000000000002"}))
	require.True(t, validResult(chat.AdmissionResult{State: "rejected", Code: "control_conflict"}))
}
