# Event protocol v1

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

## Writer contract (implemented in the events PR)

Every process acquires an OS advisory lock for the stream while allocating its next sequence and appending the record. The stream's last complete record is the counter source; no in-memory counter is authoritative. A crash leaving an incomplete final record is repaired under the same lock before the next append. A complete appended record is never assigned the same sequence twice. Concurrent child/parent activity writers share the parent's stream lock.

## Reconnect contract

Start tailing at the saved byte offset *before* querying state, buffer events, render the authoritative state, then apply only events with seq greater than state.lastSeq. A sequence gap triggers a new state query. Do not parse an incomplete line until its LF arrives. Streaming is display-only and may lose data; final answers come from state/results. Activity retries can restart a turn, so a fresh message_start invalidates the old partial display.

Full transcripts, partial text and command logs are artifacts, never embedded into workflow history. Event payloads and summaries are bounded; large output is referenced by path.
