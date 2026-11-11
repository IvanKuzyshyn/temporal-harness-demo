package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

type approvalFake struct {
	state     workflows.State
	err       error
	decisions []workflows.DecisionInput
	queries   int
}

func (f *approvalFake) Query(context.Context, string) (workflows.State, error) {
	f.queries++
	return f.state, f.err
}
func (f *approvalFake) Decide(_ context.Context, _ string, d workflows.DecisionInput) error {
	f.decisions = append(f.decisions, d)
	return f.err
}
func TestApprovalCommandParsing(t *testing.T) {
	t.Setenv("USER", "alice")
	for _, approved := range []bool{true, false} {
		var out bytes.Buffer
		if err := approvalCommand([]string{"--help"}, &out, approved); err != nil || !strings.Contains(out.String(), "<session> <requestId>") {
			t.Fatalf("help=%q %v", out.String(), err)
		}
	}
	for _, args := range [][]string{nil, {"session"}, {"session", "request", "extra"}, {"session", "request", "--unknown"}, {"session", "request", "--reason"}, {"session", "request", "--user", ""}} {
		if _, _, err := parseApprovalArgs(args, &bytes.Buffer{}, true); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	if _, _, err := parseApprovalArgs([]string{"session", "request"}, &bytes.Buffer{}, false); err == nil {
		t.Fatal("deny without reason accepted")
	}
	opts, _, err := parseApprovalArgs([]string{"session", "request", "--reason", "not safe", "--user=bob"}, &bytes.Buffer{}, false)
	if err != nil || opts.user != "bob" || opts.reason != "not safe" {
		t.Fatalf("opts=%+v %v", opts, err)
	}
	t.Setenv("USER", "")
	if _, _, err := parseApprovalArgs([]string{"session", "request"}, &bytes.Buffer{}, true); err == nil {
		t.Fatal("empty identity accepted")
	}
}
func TestApprovalCommandAuthoritativeDecision(t *testing.T) {
	t.Setenv("USER", "alice")
	for _, mode := range []string{"approve", "deny", "missing", "stale", "expired", "decided", "query error", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			approved := mode != "deny"
			args := []string{"session", "request"}
			if !approved {
				args = append(args, "--reason", "not safe")
			}
			opts, _, err := parseApprovalArgs(args, &bytes.Buffer{}, approved)
			if err != nil {
				t.Fatal(err)
			}
			f := &approvalFake{state: workflows.State{PendingApproval: &workflows.PendingApproval{RequestID: "request", ActionHash: "authoritative-hash", ExpiresAt: time.Now().Add(time.Minute)}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "missing":
				f.state.PendingApproval = nil
			case "stale":
				f.state.PendingApproval.RequestID = "other"
			case "expired":
				f.state.PendingApproval.ExpiresAt = time.Now().Add(-time.Second)
			case "decided":
				f.state.PendingApproval.Decision = &workflows.DecisionInput{}
			case "query error":
				f.err = errors.New("offline")
			case "canceled":
				cancel()
			}
			var out bytes.Buffer
			err = recordApproval(ctx, f, opts, &out, approved)
			valid := mode == "approve" || mode == "deny"
			if valid {
				if err != nil || len(f.decisions) != 1 || out.String() != "recorded\n" {
					t.Fatalf("%v %+v %q", err, f.decisions, out.String())
				}
				d := f.decisions[0]
				if d.ActionHash != "authoritative-hash" || d.User != "alice" || d.Approved != approved || d.Reason != opts.reason {
					t.Fatal(d)
				}
			} else if err == nil || len(f.decisions) != 0 || out.Len() != 0 {
				t.Fatalf("unsafe decision %v %+v %q", err, f.decisions, out.String())
			}
		})
	}
}
