-- Original byte identities remain immutable; extraction is a versioned,
-- idempotent projection, never an implicit upload-time content read.
ALTER TABLE attachments ADD COLUMN extraction_result jsonb;
ALTER TABLE attachments ADD CONSTRAINT bounded_extraction_result CHECK (
 extraction_result IS NULL OR (jsonb_typeof(extraction_result)='object' AND octet_length(extraction_result::text)<=2097152)
);
