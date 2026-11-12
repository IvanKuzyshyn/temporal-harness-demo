BEGIN;
ALTER TABLE conversations ADD COLUMN approval_toolset text NOT NULL DEFAULT '' CHECK(octet_length(approval_toolset)<=128);
ALTER TABLE chat_model_results DROP CONSTRAINT chat_model_results_pkey;
ALTER TABLE chat_model_results ADD COLUMN logical_step integer NOT NULL DEFAULT 0 CHECK(logical_step>=0 AND logical_step<12);
ALTER TABLE chat_model_results ADD COLUMN calls jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(calls)='array' AND jsonb_array_length(calls)<=4 AND octet_length(calls::text)<=40000);
ALTER TABLE chat_model_results ADD PRIMARY KEY(turn_id,logical_step);
ALTER TABLE tool_calls ADD COLUMN web_scope jsonb;
ALTER TABLE tool_calls ADD COLUMN authorized_scope jsonb;
ALTER TABLE approvals ADD COLUMN scope jsonb;
ALTER TABLE approvals ADD COLUMN outcome text CHECK(outcome IN ('authorized','denied','expired','cancelled','revoked'));
CREATE UNIQUE INDEX one_pending_web_approval ON approvals(conversation_id) WHERE scope IS NOT NULL AND outcome IS NULL;
CREATE TABLE web_tool_invocations (
 id uuid PRIMARY KEY,
 conversation_id uuid NOT NULL,
 turn_id uuid NOT NULL,
 tool_call_id uuid NOT NULL,
 action_hash text NOT NULL,
 attempt integer NOT NULL CHECK(attempt>0),
 status text NOT NULL DEFAULT 'running' CHECK(status IN ('running','completed','failed','cancelled')),
 registered_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 cleanup_confirmed_at timestamptz,
 FOREIGN KEY(conversation_id,turn_id) REFERENCES turns(conversation_id,id),
 FOREIGN KEY(conversation_id,tool_call_id,action_hash) REFERENCES tool_calls(conversation_id,id,action_hash)
);
ALTER TABLE tool_calls ADD COLUMN result_owner uuid REFERENCES web_tool_invocations(id);
CREATE INDEX outstanding_web_tools ON web_tool_invocations(conversation_id,turn_id) WHERE cleanup_confirmed_at IS NULL;
COMMIT;
