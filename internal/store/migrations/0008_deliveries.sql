CREATE TABLE deliveries (proposal_id TEXT NOT NULL REFERENCES proposals(id), device_id INTEGER NOT NULL REFERENCES devices(id),
  fetched_at TEXT NOT NULL, acked_at TEXT, PRIMARY KEY (proposal_id, device_id));
