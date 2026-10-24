CREATE TABLE goals (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, date TEXT NOT NULL, sport TEXT NOT NULL,
  distance_km REAL, elevation_gain_m REAL, target TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL, created_at TEXT NOT NULL, archived_at TEXT);
CREATE UNIQUE INDEX goals_one_active ON goals((archived_at IS NULL)) WHERE archived_at IS NULL;

CREATE TABLE notes (id INTEGER PRIMARY KEY AUTOINCREMENT, category TEXT NOT NULL, text TEXT NOT NULL,
  created_by TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);

CREATE TABLE proposals (id TEXT PRIMARY KEY, date TEXT NOT NULL, sport TEXT NOT NULL, name TEXT NOT NULL,
  content_json TEXT NOT NULL, content_hash TEXT NOT NULL, rationale TEXT NOT NULL DEFAULT '', replaces TEXT REFERENCES proposals(id),
  warnings_json TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL, status_at TEXT NOT NULL,
  created_at TEXT NOT NULL, created_lane TEXT NOT NULL, created_by TEXT NOT NULL, created_client TEXT NOT NULL);
CREATE INDEX proposals_date ON proposals(date);
CREATE INDEX proposals_status ON proposals(status);
