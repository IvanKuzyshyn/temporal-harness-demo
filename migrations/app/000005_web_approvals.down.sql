BEGIN;
ALTER TABLE tool_calls DROP COLUMN result_owner;
DROP TABLE web_tool_invocations;
DROP INDEX one_pending_web_approval;
ALTER TABLE approvals DROP COLUMN scope, DROP COLUMN outcome;
ALTER TABLE tool_calls DROP COLUMN web_scope, DROP COLUMN authorized_scope;
-- Deliberately refuses a lossy downgrade if multi-step content exists.
ALTER TABLE chat_model_results DROP CONSTRAINT chat_model_results_pkey;
ALTER TABLE chat_model_results ADD PRIMARY KEY(turn_id);
ALTER TABLE chat_model_results DROP COLUMN logical_step, DROP COLUMN calls;
ALTER TABLE conversations DROP COLUMN approval_toolset;
COMMIT;
