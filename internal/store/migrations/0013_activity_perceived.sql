-- The runner's post-activity self-evaluation from the FIT session: feel
-- 0-100 and RPE 0-10. They are written with the activity detail, so the
-- detail cache is reset to fill them from the stored FIT files.
ALTER TABLE activities ADD COLUMN perceived_feel INTEGER;
ALTER TABLE activities ADD COLUMN perceived_rpe REAL;
DELETE FROM activity_detail;
