// Package dispatch delivers durable intent at least once. It never executes a
// model itself and never equates a workflow receipt with assistant completion.
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
)

// CommandID MUST be the Temporal Update ID on every retry, including after an
// ambiguous timeout or Continue-As-New. RequestID is the original application
// deduplication identity; neither it nor the digest may be regenerated.
// Sequence is per-conversation acceptance order (not execution priority).
type Command = chat.SessionCommand

// CommandResult describes workflow admission, NOT turn completion. A nil error
// requires a valid result. Pending means accepted delivery but adjudication is
// still unknown; the same Update identity will be queried/submitted again.
// Code is a bounded public reason, never a raw upstream error or SQL message.
type CommandResult = chat.AdmissionResult
type SessionGateway interface {
	Submit(context.Context, Command) (CommandResult, error)
}
type Repository interface {
	ClaimCommands(context.Context, int, time.Duration) ([]storage.CommandClaim, error)
	SettleClaim(context.Context, storage.CommandClaim, string, []byte, time.Duration) error
}
type Options struct{ GatewayTimeout, StorageTimeout, LeaseDuration, RetryBase, RetryMax, PollInterval time.Duration }
type Dispatcher struct {
	repo    Repository
	gateway SessionGateway
	opts    Options
}

func New(repo Repository, gateway SessionGateway, opts Options) (*Dispatcher, error) {
	if repo == nil || gateway == nil {
		return nil, errors.New("dispatcher requires repository and gateway")
	}
	if opts.GatewayTimeout == 0 {
		opts.GatewayTimeout = 10 * time.Second
	}
	if opts.StorageTimeout == 0 {
		opts.StorageTimeout = 5 * time.Second
	}
	if opts.LeaseDuration == 0 {
		opts.LeaseDuration = 30 * time.Second
	}
	if opts.RetryBase == 0 {
		opts.RetryBase = time.Second
	}
	if opts.RetryMax == 0 {
		opts.RetryMax = time.Minute
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = 250 * time.Millisecond
	}
	if opts.StorageTimeout < time.Millisecond || opts.StorageTimeout > time.Minute || opts.GatewayTimeout < time.Millisecond || opts.GatewayTimeout > time.Minute || opts.StorageTimeout*2+opts.GatewayTimeout >= opts.LeaseDuration || opts.LeaseDuration < time.Second || opts.LeaseDuration > time.Minute || opts.GatewayTimeout >= opts.LeaseDuration/2 || opts.RetryBase < time.Millisecond || opts.RetryMax < opts.RetryBase || opts.RetryMax > time.Hour || opts.PollInterval < time.Millisecond {
		return nil, errors.New("invalid dispatcher bounds")
	}
	return &Dispatcher{repo: repo, gateway: gateway, opts: opts}, nil
}
func validResult(r CommandResult) bool {
	if r.TargetTurnID != "" && (r.State != "accepted" || !chat.ValidID(string(r.TargetTurnID))) {
		return false
	}
	if r.ReplacementTurnID != "" && (r.TargetTurnID == "" || !chat.ValidID(string(r.ReplacementTurnID))) {
		return false
	}
	if r.State != "pending" && r.State != "accepted" && r.State != "rejected" {
		return false
	}
	switch r.Code {
	case "", "stale_target", "invalid_approval", "approval_expired", "conversation_closed", "policy_conflict", "control_conflict", "invalid_command":
		return r.State != "rejected" || r.Code != ""
	}
	return false
}

// RunOnce claims only ONE command, so lease time is not consumed waiting behind
// network calls for other claimed items. Errors before/after remote acceptance
// are indistinguishable and retry the exact same identity with bounded backoff.
func (d *Dispatcher) RunOnce(ctx context.Context) (bool, error) {
	claimCtx, claimCancel := context.WithTimeout(ctx, d.opts.StorageTimeout)
	claims, err := d.repo.ClaimCommands(claimCtx, 1, d.opts.LeaseDuration)
	claimCancel()
	if err != nil || len(claims) == 0 {
		return false, err
	}
	c := claims[0]
	in := c.Command.Input
	a := c.Command.Receipt
	callCtx, cancel := context.WithTimeout(ctx, d.opts.GatewayTimeout)
	result, submitErr := d.gateway.Submit(callCtx, Command{CommandID: a.CommandID, RequestID: in.RequestID, ConversationID: in.ConversationID, TurnID: a.TurnID, WorkflowID: c.WorkflowID, Model: c.Model, PayloadDigest: a.PayloadDigest, Kind: in.Kind, Payload: in.Payload, Sequence: c.Sequence})
	cancel()
	delay := d.opts.RetryBase
	for n := 1; n < c.Attempts && delay < d.opts.RetryMax; n++ {
		delay *= 2
		if delay > d.opts.RetryMax {
			delay = d.opts.RetryMax
		}
	}
	state := ""
	var body []byte
	if submitErr == nil && validResult(result) {
		state = result.State
		body, _ = json.Marshal(result)
	}
	// On shutdown do not remove intents or use an unbounded background ack. The
	// expiring lease recovers even if this context is already cancelled.
	ackCtx, ackCancel := context.WithTimeout(ctx, d.opts.StorageTimeout)
	defer ackCancel()
	err = d.repo.SettleClaim(ackCtx, c, state, body, delay)
	if errors.Is(err, storage.ErrLeaseLost) {
		return true, nil
	}
	return true, err
}
