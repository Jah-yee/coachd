-- Short messages an agent sends to the runner's watch (send_message); only the newest are kept.
CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, text TEXT NOT NULL,
  created_by TEXT NOT NULL, created_at TEXT NOT NULL);
