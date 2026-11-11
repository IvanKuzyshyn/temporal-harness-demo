BEGIN;
DROP TRIGGER conversation_activity ON conversations;
DROP FUNCTION record_conversation_activity();
DROP TABLE conversation_activity;
DROP TABLE sidebar_clock;
DROP TRIGGER command_sequence ON commands;
DROP FUNCTION allocate_command_sequence();
DROP INDEX commands_due;
ALTER TABLE commands DROP CONSTRAINT commands_lease_pair, DROP CONSTRAINT commands_sequence,
 DROP COLUMN sequence, DROP COLUMN lease_token, DROP COLUMN lease_until, DROP COLUMN attempts, DROP COLUMN next_attempt_at;
ALTER TABLE conversations DROP COLUMN command_sequence, DROP COLUMN create_request_id;
COMMIT;
