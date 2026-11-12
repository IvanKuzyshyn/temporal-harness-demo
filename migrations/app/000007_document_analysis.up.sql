CREATE TABLE document_analysis_parts (
 tool_call_id uuid NOT NULL REFERENCES tool_calls(id),
 part_index integer NOT NULL CHECK (part_index BETWEEN -1 AND 31),
 input jsonb NOT NULL,
 result jsonb NOT NULL,
 owner uuid NOT NULL REFERENCES web_tool_invocations(id),
 PRIMARY KEY(tool_call_id,part_index)
);
