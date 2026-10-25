-- The detail cache gains the workout an activity recorded, and its
-- fingerprint for linking it to a delivered proposal. The cached shape
-- changes, so the cache is reset: it rebuilds from the stored FIT files.
ALTER TABLE activity_detail ADD COLUMN workout_json TEXT;
ALTER TABLE activity_detail ADD COLUMN workout_fp TEXT;
DELETE FROM activity_detail;
