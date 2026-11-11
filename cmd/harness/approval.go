package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	hc "github.com/IvanKuzyshyn/temporal-harness-demo/internal/client"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

type approvalOptions struct{ session, request, user, reason string }
type approvalBackend interface {
	Query(context.Context, string) (workflows.State, error)
	Decide(context.Context, string, workflows.DecisionInput) error
}

// approvalCommand is the standalone package-main seam for approve/deny. The
// dispatcher is owned by integration; args exclude "harness approve/deny".
func approvalCommand(args []string, out io.Writer, approved bool) error {
	opts, help, err := parseApprovalArgs(args, out, approved)
	if err != nil || help {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	client, err := hc.Dial(ctx, cfg.TemporalAddress)
	if err != nil {
		return err
	}
	defer client.Close()
	return recordApproval(ctx, client, opts, out, approved)
}

func parseApprovalArgs(args []string, out io.Writer, approved bool) (approvalOptions, bool, error) {
	opts := approvalOptions{user: strings.TrimSpace(os.Getenv("USER"))}
	name := "approve"
	if !approved {
		name = "deny"
	}
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--help" || arg == "-h" {
			_, err := fmt.Fprintf(out, "Usage: harness %s <session> <requestId> [--user USER]", name)
			if err == nil {
				_, err = fmt.Fprintln(out, " [--reason REASON]\n--user defaults to $USER; deny requires a nonempty --reason.\nThe action hash is read from authoritative session state.")
			}
			return opts, true, err
		}
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		flag, value, hasValue := strings.Cut(arg, "=")
		if flag != "--user" && flag != "--reason" {
			return opts, false, fmt.Errorf("%s: unknown option %s", name, flag)
		}
		if !hasValue {
			i++
			if i >= len(args) || strings.HasPrefix(args[i], "--") {
				return opts, false, fmt.Errorf("%s requires a value", flag)
			}
			value = args[i]
		}
		if flag == "--user" {
			opts.user = strings.TrimSpace(value)
		} else {
			opts.reason = value
		}
	}
	if len(positional) != 2 || strings.TrimSpace(positional[0]) == "" || strings.TrimSpace(positional[1]) == "" {
		return opts, false, fmt.Errorf("usage: harness %s <session> <requestId>", name)
	}
	opts.session, opts.request = positional[0], positional[1]
	if opts.user == "" {
		return opts, false, fmt.Errorf("approval user is required (--user or $USER)")
	}
	if !approved && strings.TrimSpace(opts.reason) == "" {
		return opts, false, fmt.Errorf("deny requires --reason")
	}
	if len(opts.user) > 256 || len(opts.reason) > 4096 {
		return opts, false, fmt.Errorf("approval user or reason too large")
	}
	return opts, false, nil
}

func recordApproval(ctx context.Context, backend approvalBackend, opts approvalOptions, out io.Writer, approved bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := backend.Query(ctx, opts.session)
	if err != nil {
		return err
	}
	p := state.PendingApproval
	if p == nil || p.RequestID != opts.request || p.ActionHash == "" || p.Decision != nil || !time.Now().Before(p.ExpiresAt) {
		return fmt.Errorf("unknown or stale approval request")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// $USER/--user is a local display identity. Real authentication belongs at
	// the caller/service boundary. The workflow validator closes Query/Decide races.
	d := workflows.DecisionInput{RequestID: opts.request, ActionHash: p.ActionHash, Approved: approved, User: opts.user, Reason: opts.reason}
	if err := backend.Decide(ctx, opts.session, d); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "recorded")
	return err
}
