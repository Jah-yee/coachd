-- Runner auth: passkeys, one-time enrolments, web sessions and used approval/ceremony nonces.
CREATE TABLE passkeys (id INTEGER PRIMARY KEY AUTOINCREMENT, credential_id BLOB NOT NULL UNIQUE,
  credential_json TEXT NOT NULL, label TEXT NOT NULL, created_at TEXT NOT NULL, last_used_at TEXT, revoked_at TEXT);
CREATE TABLE enrollments (id INTEGER PRIMARY KEY AUTOINCREMENT, token_hash BLOB NOT NULL UNIQUE,
  created_at TEXT NOT NULL, expires_at TEXT NOT NULL, used_at TEXT);
CREATE TABLE web_sessions (token_hash BLOB PRIMARY KEY, passkey_id INTEGER NOT NULL REFERENCES passkeys(id),
  created_at TEXT NOT NULL, expires_at TEXT NOT NULL);
CREATE TABLE used_nonces (nonce TEXT PRIMARY KEY, used_at TEXT NOT NULL, expires_at TEXT NOT NULL);
