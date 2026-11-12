BEGIN;
DROP TABLE chat_model_invocations;
ALTER TABLE turns DROP COLUMN model_invocations_closed;
COMMIT;
