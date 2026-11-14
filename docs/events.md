# Historical: terminal event protocol v1

> Retired JSONL display transport. Web lifecycle events use SQL/SSE; see [web streaming](web-chat-streaming.md). The historical protocol below is not shipped.

Each record is one UTF-8 JSON object followed by LF. Fields are `seq` (uint64, strictly increasing per stream), `ts` (RFC3339Nano), `workflowId`, `runId`, `parentWorkflowId` (empty for parent), `type`, `pattern`, `data`.

`pattern` is `parallel`, `child`, `long_running`, `approval`, or `none`. `internal/events` defines the envelope, complete type inventory and typed data structures.

## Payloads

- `agent_start`: inputId
- `turn_start`, `turn_end`: turn, toolResults (artifact refs)
- `message_start`, `message_end`: role, messageRef
- `message_update`: delta, tokens
- `tool_execution_start`: toolCallId, name, args, executionMode, details
- `tool_execution_update`: toolCallId, chunk, offset
- `tool_execution_end`: toolCallId, isError, interrupted, summary
- `approval_requested`, `approval_resolved`: requestId, actionHash, command, decision, user, reason
- `child_started`, `child_completed`: childWorkflowId, task, summaryRef
- `parallel_batch_start`, `parallel_batch_end`: toolCallIds, concurrency
- `heartbeat`: activity, progress
- `agent_end`: reason
- `agent_settled`: status (done or failed), optional runFailure
- `run_failed`: reason (model_error, aborted, interrupted, restart_limit, turn_limit, nothing_to_answer)

## Examples

```jsonl
{"seq":1,"ts":"2026-11-09T00:00:00Z","workflowId":"session-demo","runId":"r1","parentWorkflowId":"","type":"agent_start","pattern":"none","data":{"inputId":"input-1"}}
{"seq":2,"ts":"2026-11-09T00:00:01Z","workflowId":"session-demo","runId":"r1","parentWorkflowId":"","type":"parallel_batch_start","pattern":"parallel","data":{"toolCallIds":["git","tree","readme","todo"],"concurrency":4}}
{"seq":3,"ts":"2026-11-09T00:00:02Z","workflowId":"session-demo/explore-1","runId":"r2","parentWorkflowId":"session-demo","type":"heartbeat","pattern":"long_running","data":{"activity":"LLMStream","progress":12}}
{"seq":4,"ts":"2026-11-09T00:00:03Z","workflowId":"session-demo","runId":"r1","parentWorkflowId":"","type":"agent_settled","pattern":"none","data":{"status":"done"}}
```

## Writer contract

Every process acquires an OS advisory lock for the stream while allocating its next sequence and appending the record. The stream's last complete record is the counter source; no in-memory counter is authoritative. A crash leaving an incomplete final record is repaired under the same lock before the next append. A complete appended record is never assigned the same sequence twice. Concurrent child/parent activity writers share the parent's stream lock.

`NewStore(dataDir)` exposes `Path(streamID) (string, error)` and `Append(scope, type, pattern, data) (Event, error)`. Files live in `dataDir/streams/<lowercase-hex-of-UTF-8-ID>.jsonl`; slash-containing child IDs are encoded without creating directories, and differently cased IDs remain distinct on case-insensitive filesystems. IDs are limited to 124 UTF-8 bytes so filenames fit within 255 bytes. This encoding replaces URL-path escaping; existing URL-escaped stream files require offline migration before reuse. Empty, dot, traversal, control-character, backslash, invalid UTF-8, and overlong encoded IDs are rejected. `Scope.StreamID` selects the stream; the emitting child's workflow/run/parent envelope is retained. Timestamps are generated activity-side in UTC RFC3339Nano.

Writers use Darwin/Linux `flock`, inspect at most the final 2 MiB, and repair a partial trailing line before appending. Records including LF are capped at 1 MiB. Malformed complete records and oversized crash fragments produce explicit errors rather than silently resetting the sequence. There is no fsync durability guarantee; data directories must be trusted, on a filesystem supporting advisory locks, and all writers must follow this protocol. Older corruption outside the suffix is not scanned on append.

`Tail(path, offset) ([]Event, int64, error)` reads a file-size snapshot, at most 1024 complete records, leaving the offset at a partial or malformed line. Missing files are harmless; shrinkage below the offset returns `ErrTruncated`. Callers must reset/query state on truncation; replacement or truncate-and-regrow above the offset cannot be detected from a byte offset alone.

For reconnect, create `NewReconnect()`, call `Buffer([]Event) error` while the state query is pending, then `Resolve(snapshotLastSeq uint64) ([]Event, error)`. Resolution discards stale records and returns only contiguous newer records, or an error wrapping `ErrGap` (no partial result). Buffering is capped at 4096 events; overflow also requires a fresh query. The helper is single-owner, not goroutine-safe, and one instance represents one query. Continue live sequence tracking from the last returned sequence.

## Reconnect contract

Start tailing at the saved byte offset *before* querying state, buffer events, render the authoritative state, then apply only events with seq greater than state.lastSeq. A sequence gap triggers a new state query. Do not parse an incomplete line until its LF arrives. Streaming is display-only and may lose data; final answers come from state/results. Activity retries can restart a turn, so a fresh message_start invalidates the old partial display.

Full transcripts, partial text and command logs are artifacts, never embedded into workflow history. Event payloads and summaries are bounded; large output is referenced by path.
