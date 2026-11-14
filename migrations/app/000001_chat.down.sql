BEGIN;
DROP TABLE events, commands, conversation_summaries, approvals, tool_calls, model_steps, message_attachments, messages, attachments, turns, conversations;
DROP FUNCTION ready_chat_attachment(), immutable_chat_entry(), allocate_chat_sequence();
COMMIT;
