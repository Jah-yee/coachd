CREATE TABLE activity_detail (
	activity_id INTEGER PRIMARY KEY REFERENCES activities(id) ON DELETE CASCADE,
	laps_json   TEXT NOT NULL,
	series_json TEXT NOT NULL,
	computed_at TEXT NOT NULL
);
