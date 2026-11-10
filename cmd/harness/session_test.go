package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/api/serviceerror"
)

type listFake struct{ cliFake }

func (f *listFake) Sessions(context.Context) ([]string, error) {
	return []string{"prototype", "healthy"}, nil
}

func TestListQueryFailedContinues(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		for _, asJSON := range []bool{false, true} {
			t.Run(fmt.Sprintf("wrapped=%t/json=%t", wrapped, asJSON), func(t *testing.T) {
				var queryErr error = serviceerror.NewQueryFailed("[TMPRL1100] lookup failed for scheduledEventID to activityID: scheduleEventID: 31, activityID: 31")
				if wrapped {
					queryErr = fmt.Errorf("query: %w", queryErr)
				}
				f := &listFake{cliFake{queryErrors: map[int]error{0: queryErr}, states: []workflows.State{{}, {Status: "working", Turn: 3, Answer: "not listed"}}}}
				var out bytes.Buffer
				if err := listSessions(context.Background(), f, &out, asJSON); err != nil {
					t.Fatal(err)
				}
				if f.queries != 2 {
					t.Fatalf("queries=%d", f.queries)
				}
				if !asJSON {
					if want := "prototype\tquery/unavailable\tturn=0\nhealthy\tworking\tturn=3\n"; out.String() != want {
						t.Fatalf("output=%q", out.String())
					}
					return
				}
				dec := json.NewDecoder(&out)
				for _, want := range []cliSummary{{Type: "session_state", ID: "prototype", Status: "query/unavailable"}, {Type: "session_state", ID: "healthy", Status: "working", Turn: 3}} {
					var got cliSummary
					if err := dec.Decode(&got); err != nil {
						t.Fatal(err)
					}
					if got != want {
						t.Fatalf("got %+v want %+v", got, want)
					}
				}
				var extra cliSummary
				if err := dec.Decode(&extra); err != io.EOF {
					t.Fatalf("extra record: %+v, %v", extra, err)
				}
			})
		}
	}
}

func TestListQueryErrorsPropagate(t *testing.T) {
	for name, cause := range map[string]error{
		"permission":       serviceerror.NewPermissionDenied("denied", ""),
		"transport":        serviceerror.NewUnavailable("offline"),
		"unknown":          errors.New("[TMPRL1100] lookup failed for scheduledEventID to activityID"),
		"invalid argument": serviceerror.NewInvalidArgument("query failed"),
		"canceled":         context.Canceled,
		"deadline":         context.DeadlineExceeded,
	} {
		for _, asJSON := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", name, asJSON), func(t *testing.T) {
				f := &listFake{cliFake{queryErrors: map[int]error{0: fmt.Errorf("wrapped: %w", cause)}}}
				var out bytes.Buffer
				err := listSessions(context.Background(), f, &out, asJSON)
				if !errors.Is(err, cause) {
					t.Fatalf("got %v want %v", err, cause)
				}
				if out.Len() != 0 || f.queries != 1 {
					t.Fatalf("output=%q queries=%d", out.String(), f.queries)
				}
			})
		}
	}
}
