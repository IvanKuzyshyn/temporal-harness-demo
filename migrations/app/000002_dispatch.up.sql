BEGIN;
-- Keep applied migration 000001 immutable. Existing commands acquire a stable
-- per-conversation order; new allocations serialize with command acceptance.
ALTER TABLE conversations ADD COLUMN command_sequence bigint NOT NULL DEFAULT 0;
ALTER TABLE conversations ADD COLUMN create_request_id uuid UNIQUE;
ALTER TABLE commands ADD COLUMN sequence bigint;
WITH ranked AS (SELECT id,row_number() OVER (PARTITION BY conversation_id ORDER BY created_at,id) AS n FROM commands)
UPDATE commands SET sequence=ranked.n FROM ranked WHERE ranked.id=commands.id;
UPDATE conversations c SET command_sequence=coalesce((SELECT max(sequence) FROM commands WHERE conversation_id=c.id),0);
ALTER TABLE commands ALTER COLUMN sequence SET NOT NULL;
ALTER TABLE commands ADD CONSTRAINT commands_sequence UNIQUE(conversation_id,sequence);
ALTER TABLE commands ADD COLUMN lease_token uuid;
ALTER TABLE commands ADD COLUMN lease_until timestamptz;
ALTER TABLE commands ADD COLUMN attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0);
ALTER TABLE commands ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp();
ALTER TABLE commands ADD CONSTRAINT commands_lease_pair CHECK((lease_token IS NULL)=(lease_until IS NULL));
CREATE INDEX commands_due ON commands(next_attempt_at,created_at,id) WHERE result_state='pending';
CREATE FUNCTION allocate_command_sequence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE conversations SET command_sequence=command_sequence+1,updated_at=clock_timestamp()
 WHERE id=NEW.conversation_id RETURNING command_sequence INTO NEW.sequence;
 RETURN NEW;
END $$;
CREATE TRIGGER command_sequence BEFORE INSERT ON commands FOR EACH ROW EXECUTE FUNCTION allocate_command_sequence();
-- A compact activity index preserves sidebar snapshot membership/order even
-- when later writes move an unseen conversation to the head of the live list.
-- Counter row allocation is transactional: no commit can appear behind a cursor.
CREATE TABLE sidebar_clock (singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), sequence bigint NOT NULL);
INSERT INTO sidebar_clock VALUES(true,0);
CREATE TABLE conversation_activity (sequence bigint PRIMARY KEY, conversation_id uuid NOT NULL REFERENCES conversations(id));
CREATE INDEX conversation_activity_snapshot ON conversation_activity(conversation_id,sequence DESC);
INSERT INTO conversation_activity SELECT row_number() OVER(ORDER BY updated_at,id),id FROM conversations;
UPDATE sidebar_clock SET sequence=(SELECT count(*) FROM conversations);
CREATE FUNCTION record_conversation_activity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE n bigint;
BEGIN
 UPDATE sidebar_clock SET sequence=sequence+1 WHERE singleton RETURNING sequence INTO n;
 INSERT INTO conversation_activity VALUES(n,NEW.id);
 RETURN NEW;
END $$;
CREATE TRIGGER conversation_activity AFTER INSERT OR UPDATE OF updated_at ON conversations FOR EACH ROW EXECUTE FUNCTION record_conversation_activity();
COMMIT;
