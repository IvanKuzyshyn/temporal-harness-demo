BEGIN;
-- Independent of the dispatcher lease/ack. Written only by a workflow activity
-- scheduled AFTER durable enqueue, never by API acceptance or a gateway.
ALTER TABLE commands ADD COLUMN execution_receipt jsonb CHECK (
 execution_receipt IS NULL OR (jsonb_typeof(execution_receipt)='object' AND execution_receipt->>'state' IN ('accepted','rejected')));
ALTER TABLE turns ADD COLUMN settlement_digest text CHECK (settlement_digest ~ '^[0-9a-f]{64}$');
ALTER TABLE model_steps ADD CONSTRAINT model_steps_result_owner UNIQUE(conversation_id,turn_id,id);
CREATE TABLE chat_model_results (
 conversation_id uuid NOT NULL,
 turn_id uuid PRIMARY KEY,
 text text NOT NULL CHECK(octet_length(text)<=65536),
 tokens integer NOT NULL CHECK(tokens>=0),
 attempt_id uuid,
 limited_history boolean NOT NULL,
 payload_digest text NOT NULL CHECK(payload_digest ~ '^[0-9a-f]{64}$'),
 FOREIGN KEY(conversation_id,turn_id) REFERENCES turns(conversation_id,id),
 FOREIGN KEY(conversation_id,turn_id,attempt_id) REFERENCES model_steps(conversation_id,turn_id,id)
);
CREATE TRIGGER immutable_chat_model_results BEFORE UPDATE OR DELETE ON chat_model_results FOR EACH ROW EXECUTE FUNCTION immutable_chat_entry();
CREATE INDEX completed_chat_turns ON turns(conversation_id,created_at,id) WHERE status='completed';
COMMIT;
