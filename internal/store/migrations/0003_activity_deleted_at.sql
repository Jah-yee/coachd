-- Soft delete: reconcile marks activities Garmin confirmed deleted; the row,
-- its raw JSON, detail and FIT file are kept. Every read filters
-- deleted_at IS NULL. No index: deleted rows are rare, and the date index plus
-- a residual filter serves the read queries.
ALTER TABLE activities ADD COLUMN deleted_at TEXT;
