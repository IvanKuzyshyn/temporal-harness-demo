BEGIN;
CREATE TABLE conversations (
 id uuid PRIMARY KEY,
 title text NOT NULL DEFAULT '',
 model text NOT NULL CHECK (model IN ('qwen3.5:9b','gemma4:e4b')),
 workflow_id text NOT NULL UNIQUE,
 lifecycle text NOT NULL DEFAULT 'open' CHECK (lifecycle IN ('open','closed')),
 policy_revision bigint NOT NULL DEFAULT 0 CHECK (policy_revision >= 0),
 message_sequence bigint NOT NULL DEFAULT 0,
 event_sequence bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(id,model)
);
CREATE INDEX conversations_listing ON conversations(updated_at DESC,id);
CREATE TABLE turns (
 id uuid PRIMARY KEY,
 conversation_id uuid NOT NULL REFERENCES conversations(id),
 request_id uuid NOT NULL,
 kind text NOT NULL CHECK (kind IN ('message','replacement')),
 replacement_target uuid,
 status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','awaiting_approval','cancelling','completed','cancelled','failed')),
 reason text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(conversation_id,id), UNIQUE(conversation_id,request_id),
 FOREIGN KEY(conversation_id,replacement_target) REFERENCES turns(conversation_id,id),
 CHECK ((kind='replacement') = (replacement_target IS NOT NULL))
);
CREATE INDEX turns_listing ON turns(conversation_id,created_at,id);
CREATE TABLE attachments (
 id uuid PRIMARY KEY,
 conversation_id uuid NOT NULL REFERENCES conversations(id),
 original_name text NOT NULL,
 media_type text NOT NULL CHECK(media_type IN ('text/plain','text/markdown','application/pdf','application/vnd.openxmlformats-officedocument.wordprocessingml.document','image/png','image/jpeg','image/svg+xml')),
 size bigint NOT NULL CHECK(size BETWEEN 0 AND 1048576),
 checksum text NOT NULL CHECK(checksum ~ '^[0-9a-f]{64}$'),
 storage_key text NOT NULL UNIQUE CHECK(storage_key <> ''),
 status text NOT NULL DEFAULT 'staged' CHECK(status IN ('staged','ready','failed')),
 extraction_status text NOT NULL DEFAULT 'pending' CHECK(extraction_status IN ('pending','completed','failed','unsupported')),
 extracted_text text NOT NULL DEFAULT '' CHECK(length(extracted_text)<=200000),
 derived_image_keys jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(derived_image_keys)='array'),
 payload_digest text NOT NULL CHECK(payload_digest ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(conversation_id,id)
);
CREATE INDEX attachments_listing ON attachments(conversation_id,created_at,id);
CREATE TABLE messages (
 entry_id uuid PRIMARY KEY,
 -- Internal creation transaction: attachment joins must commit with the entry.
 created_xid xid8 NOT NULL,
 conversation_id uuid NOT NULL REFERENCES conversations(id),
 turn_id uuid NOT NULL,
 sequence bigint NOT NULL CHECK(sequence>0),
 role text NOT NULL CHECK(role IN ('user','assistant')),
 text text NOT NULL,
 logical_step integer NOT NULL CHECK(logical_step>=0),
 result_kind text NOT NULL CHECK(result_kind<>''),
 completion_state text NOT NULL CHECK(completion_state IN ('completed','incomplete')),
 payload_digest text NOT NULL CHECK(payload_digest ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(conversation_id,turn_id) REFERENCES turns(conversation_id,id),
 UNIQUE(conversation_id,entry_id), UNIQUE(conversation_id,turn_id,entry_id), UNIQUE(conversation_id,sequence),
 UNIQUE(turn_id,logical_step,result_kind)
);
CREATE TABLE message_attachments (
 conversation_id uuid NOT NULL,
 entry_id uuid NOT NULL,
 attachment_id uuid NOT NULL,
 ordinal integer NOT NULL CHECK(ordinal BETWEEN 0 AND 3),
 PRIMARY KEY(entry_id,attachment_id), UNIQUE(entry_id,ordinal),
 FOREIGN KEY(conversation_id,entry_id) REFERENCES messages(conversation_id,entry_id),
 FOREIGN KEY(conversation_id,attachment_id) REFERENCES attachments(conversation_id,id)
);
CREATE TABLE model_steps (
 id uuid PRIMARY KEY,
 conversation_id uuid NOT NULL,
 turn_id uuid NOT NULL,
 logical_step integer NOT NULL CHECK(logical_step>=0),
 attempt integer NOT NULL CHECK(attempt>0),
 status text NOT NULL CHECK(status IN ('running','completed','failed','cancelled')),
 usage jsonb,
 result_entry_id uuid,
 error text NOT NULL DEFAULT '',
 payload_digest text NOT NULL CHECK(payload_digest ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(conversation_id,turn_id) REFERENCES turns(conversation_id,id),
 FOREIGN KEY(conversation_id,turn_id,result_entry_id) REFERENCES messages(conversation_id,turn_id,entry_id),
 UNIQUE(turn_id,logical_step,attempt), UNIQUE(conversation_id,turn_id,logical_step,attempt)
);
CREATE TABLE tool_calls (
 id uuid PRIMARY KEY,
 conversation_id uuid NOT NULL,
 turn_id uuid NOT NULL,
 logical_key text NOT NULL CHECK(logical_key<>''),
 name text NOT NULL CHECK(name IN ('web_fetch','file_upload')),
 arguments jsonb NOT NULL CHECK(jsonb_typeof(arguments)='object'),
 action_hash text NOT NULL CHECK(action_hash ~ '^[0-9a-f]{64}$'),
 parent_workflow_id text NOT NULL,
 child_workflow_id text,
 status text NOT NULL CHECK(status IN ('pending','running','completed','failed','denied','cancelled')),
 result jsonb,
 sources jsonb NOT NULL DEFAULT '[]',
 payload_digest text NOT NULL CHECK(payload_digest ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(conversation_id,turn_id) REFERENCES turns(conversation_id,id),
 UNIQUE(turn_id,logical_key), UNIQUE(conversation_id,id,action_hash)
);
CREATE TABLE approvals (
 id uuid PRIMARY KEY,
 conversation_id uuid NOT NULL,
 tool_call_id uuid NOT NULL,
 action_hash text NOT NULL,
 deadline timestamptz NOT NULL,
 decision text CHECK(decision IN ('once','all','deny')),
 policy_revision bigint NOT NULL CHECK(policy_revision>=0),
 decided_at timestamptz,
 payload_digest text NOT NULL CHECK(payload_digest ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(conversation_id,tool_call_id,action_hash) REFERENCES tool_calls(conversation_id,id,action_hash),
 CHECK ((decision IS NULL) = (decided_at IS NULL)),
 UNIQUE(conversation_id,id,action_hash)
);
CREATE INDEX approvals_listing ON approvals(conversation_id,deadline,id);
CREATE TABLE conversation_summaries (
 id uuid PRIMARY KEY,
 conversation_id uuid NOT NULL,
 version bigint NOT NULL CHECK(version>0),
 covered_sequence bigint NOT NULL,
 text text NOT NULL,
 model text NOT NULL,
 budget jsonb NOT NULL CHECK(jsonb_typeof(budget)='object'),
 payload_digest text NOT NULL CHECK(payload_digest ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(conversation_id,model) REFERENCES conversations(id,model),
 FOREIGN KEY(conversation_id,covered_sequence) REFERENCES messages(conversation_id,sequence),
 UNIQUE(conversation_id,version)
);
CREATE TABLE commands (
 id uuid PRIMARY KEY,
 conversation_id uuid NOT NULL REFERENCES conversations(id),
 request_id uuid NOT NULL,
 turn_id uuid,
 kind text NOT NULL CHECK(kind IN ('message','replace','stop','decide','revoke')),
 expected_active_turn_id uuid,
 approval_id uuid,
 action_hash text,
 payload jsonb NOT NULL,
 payload_digest text NOT NULL CHECK(payload_digest ~ '^[0-9a-f]{64}$'),
 dispatch_state text NOT NULL DEFAULT 'pending' CHECK(dispatch_state IN ('pending','dispatched')),
 result_state text NOT NULL DEFAULT 'pending' CHECK(result_state IN ('pending','accepted','rejected')),
 result jsonb,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(conversation_id,request_id),
 FOREIGN KEY(conversation_id,turn_id) REFERENCES turns(conversation_id,id),
 FOREIGN KEY(conversation_id,expected_active_turn_id) REFERENCES turns(conversation_id,id),
 FOREIGN KEY(conversation_id,approval_id,action_hash) REFERENCES approvals(conversation_id,id,action_hash),
 CHECK ((kind IN ('message','replace')) = (turn_id IS NOT NULL)),
 CHECK ((kind IN ('stop','replace')) = (expected_active_turn_id IS NOT NULL)),
 CHECK ((kind='decide') = (approval_id IS NOT NULL AND action_hash IS NOT NULL))
);
CREATE INDEX commands_pending ON commands(created_at,id) WHERE dispatch_state='pending' OR result_state='pending';
CREATE TABLE events (
 id uuid PRIMARY KEY,
 conversation_id uuid NOT NULL REFERENCES conversations(id),
 turn_id uuid,
 logical_step integer,
 attempt integer,
 sequence bigint NOT NULL CHECK(sequence>0),
 type text NOT NULL CHECK(type<>''),
 payload jsonb NOT NULL CHECK(octet_length(payload::text)<=65536),
 payload_digest text NOT NULL CHECK(payload_digest ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(conversation_id,turn_id) REFERENCES turns(conversation_id,id),
 FOREIGN KEY(conversation_id,turn_id,logical_step,attempt) REFERENCES model_steps(conversation_id,turn_id,logical_step,attempt),
 CHECK ((logical_step IS NULL) = (attempt IS NULL)),
 CHECK (logical_step IS NULL OR turn_id IS NOT NULL),
 UNIQUE(conversation_id,sequence)
);
-- A row lock is held until COMMIT. Even callers outside the repository cannot
-- publish sequence N+1 before N commits. Rolled-back allocations roll back too.
CREATE FUNCTION allocate_chat_sequence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='messages' THEN
  NEW.created_xid := pg_current_xact_id();
  UPDATE conversations SET message_sequence=message_sequence+1,updated_at=clock_timestamp()
   WHERE id=NEW.conversation_id RETURNING message_sequence INTO NEW.sequence;
 ELSE
  UPDATE conversations SET event_sequence=event_sequence+1,updated_at=clock_timestamp()
   WHERE id=NEW.conversation_id RETURNING event_sequence INTO NEW.sequence;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER message_sequence BEFORE INSERT ON messages FOR EACH ROW EXECUTE FUNCTION allocate_chat_sequence();
CREATE TRIGGER event_sequence BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION allocate_chat_sequence();
CREATE FUNCTION immutable_chat_entry() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'immutable chat entry' USING ERRCODE='23514'; END $$;
CREATE TRIGGER immutable_messages BEFORE UPDATE OR DELETE ON messages FOR EACH ROW EXECUTE FUNCTION immutable_chat_entry();
CREATE TRIGGER immutable_message_attachments BEFORE UPDATE OR DELETE ON message_attachments FOR EACH ROW EXECUTE FUNCTION immutable_chat_entry();
CREATE FUNCTION ready_chat_attachment() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM 1 FROM messages WHERE entry_id=NEW.entry_id AND conversation_id=NEW.conversation_id AND created_xid=pg_current_xact_id();
 IF NOT FOUND THEN RAISE EXCEPTION 'attachment references must commit with their immutable message' USING ERRCODE='23514'; END IF;
 PERFORM 1 FROM attachments WHERE id=NEW.attachment_id AND conversation_id=NEW.conversation_id AND status='ready' FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'attachment not ready in this conversation' USING ERRCODE='23503'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER ready_attachment BEFORE INSERT ON message_attachments FOR EACH ROW EXECUTE FUNCTION ready_chat_attachment();
COMMIT;
