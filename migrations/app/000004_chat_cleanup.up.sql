BEGIN;
-- Publication ownership is not cleanup evidence. Each activity invocation owns
-- a distinct registration, including duplicate deliveries of the same attempt.
ALTER TABLE turns ADD COLUMN model_invocations_closed boolean NOT NULL DEFAULT false;
CREATE TABLE chat_model_invocations (
 id uuid PRIMARY KEY,
 conversation_id uuid NOT NULL,
 turn_id uuid NOT NULL,
 attempt integer NOT NULL CHECK(attempt > 0),
 registered_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 cleanup_confirmed_at timestamptz,
 FOREIGN KEY(conversation_id,turn_id) REFERENCES turns(conversation_id,id)
);
CREATE INDEX unconfirmed_chat_invocations ON chat_model_invocations(conversation_id,turn_id) WHERE cleanup_confirmed_at IS NULL;
-- Existing nonterminal work has no cleanup evidence. Do not manufacture an ack
-- from a superseded/failed model_steps projection during upgrade. Such turns may
-- require operator investigation; workflow-history compatibility is not promised.
INSERT INTO chat_model_invocations(id,conversation_id,turn_id,attempt)
 SELECT s.id,s.conversation_id,s.turn_id,s.attempt FROM model_steps s
 JOIN turns t ON t.id=s.turn_id WHERE t.status IN ('queued','running','cancelling');
UPDATE turns SET model_invocations_closed=true WHERE status IN ('completed','failed','cancelled');
COMMIT;
