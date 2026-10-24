CREATE TABLE devices (id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT NOT NULL, model TEXT NOT NULL DEFAULT '',
  token_hash BLOB NOT NULL UNIQUE, created_at TEXT NOT NULL, last_seen_at TEXT, revoked_at TEXT);
CREATE TABLE device_pairings (id TEXT PRIMARY KEY, code TEXT NOT NULL, model TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, expires_at TEXT NOT NULL, confirmed_at TEXT, label TEXT NOT NULL DEFAULT '',
  claimed_at TEXT, device_id INTEGER REFERENCES devices(id));
CREATE INDEX device_pairings_code ON device_pairings(code);
