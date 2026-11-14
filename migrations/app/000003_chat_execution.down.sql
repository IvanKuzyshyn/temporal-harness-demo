BEGIN;
DROP INDEX completed_chat_turns;
DROP TABLE chat_model_results;
ALTER TABLE model_steps DROP CONSTRAINT model_steps_result_owner;
ALTER TABLE turns DROP COLUMN settlement_digest;
ALTER TABLE commands DROP COLUMN execution_receipt;
COMMIT;
