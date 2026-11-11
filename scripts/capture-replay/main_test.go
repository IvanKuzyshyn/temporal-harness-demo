package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestSanitizeDecodedPayloadOnly(t *testing.T) {
	const root = "/private/tmp/owned-capture"
	p, err := converter.GetDefaultDataConverter().ToPayload(map[string]string{"path": root + "/project", "id": "unchanged-run-id"})
	require.NoError(t, err)
	opaque := &commonpb.Payload{Metadata: map[string][]byte{"encoding": []byte("binary/plain")}, Data: []byte(root)}
	h := &historypb.History{Events: []*historypb.HistoryEvent{{EventId: 1, Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{Identity: "replay-capture", OriginalExecutionRunId: "unchanged-run-id", Input: &commonpb.Payloads{Payloads: []*commonpb.Payload{p, opaque}}}}}}}
	sanitize(h.ProtoReflect(), root)
	// Exercise protojson's base64 round trip; searching outer JSON is insufficient.
	b, err := protojson.Marshal(h)
	require.NoError(t, err)
	copy := &historypb.History{}
	require.NoError(t, protojson.Unmarshal(b, copy))
	input := copy.Events[0].GetWorkflowExecutionStartedEventAttributes().Input
	var decoded map[string]string
	require.NoError(t, converter.GetDefaultDataConverter().FromPayload(input.Payloads[0], &decoded))
	require.Equal(t, "/replay-capture/project", decoded["path"])
	require.Equal(t, "unchanged-run-id", decoded["id"])
	require.Equal(t, root, string(input.Payloads[1].Data), "opaque encodings are not rewritten")
	require.Equal(t, "unchanged-run-id", copy.Events[0].GetWorkflowExecutionStartedEventAttributes().OriginalExecutionRunId)
}

func TestCaptureRefusesExistingOutputBeforeStartingAnything(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sentinel")
	require.NoError(t, os.WriteFile(path, []byte("do not overwrite"), 0600))
	require.ErrorContains(t, capture(context.Background(), dir), "output must not exist")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "do not overwrite", string(b))
}

func TestCaptureCanceledBeforeToolOrServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, capture(ctx, filepath.Join(t.TempDir(), "new")), context.Canceled)
}
