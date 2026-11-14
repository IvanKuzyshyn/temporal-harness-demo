-- Enforce new writes without deleting or rewriting legacy conversations/messages.
-- NOT VALID intentionally retains historical disabled-model/multi-file rows.
ALTER TABLE conversations ADD CONSTRAINT enabled_chat_model CHECK (model IN ('qwen3.5:9b')) NOT VALID;
ALTER TABLE message_attachments ADD CONSTRAINT single_message_attachment CHECK (ordinal = 0) NOT VALID;
