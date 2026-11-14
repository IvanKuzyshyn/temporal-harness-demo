ALTER TABLE chat_model_results ADD COLUMN context_metadata jsonb;
ALTER TABLE chat_model_results ADD CONSTRAINT context_metadata_size CHECK(context_metadata IS NULL OR octet_length(context_metadata::text)<=8192);
-- Completion-order selection is independent of prompt acceptance/queue order.
CREATE INDEX messages_completed_context ON messages(conversation_id,sequence) WHERE role='assistant' AND result_kind='final' AND completion_state='completed';
