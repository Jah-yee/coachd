-- Built-in OAuth 2.1 authorization server: clients, grants, single-use codes and rotating tokens.
CREATE TABLE oauth_clients (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  client_id TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL CHECK (kind IN ('cimd','dcr')),
  name TEXT NOT NULL,
  redirect_uris TEXT NOT NULL,
  metadata TEXT NOT NULL,
  fetched_at TEXT NOT NULL,
  cache_until TEXT NOT NULL,
  created_at TEXT NOT NULL);
CREATE TABLE oauth_grants (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  client_row_id INTEGER NOT NULL REFERENCES oauth_clients(id),
  scopes TEXT NOT NULL,
  resource TEXT NOT NULL,
  passkey_id INTEGER NOT NULL REFERENCES passkeys(id),
  created_at TEXT NOT NULL,
  activated_at TEXT,
  last_used_at TEXT,
  revoked_at TEXT);
CREATE INDEX oauth_grants_client ON oauth_grants(client_row_id);
CREATE TABLE oauth_codes (
  hash BLOB PRIMARY KEY,
  grant_id INTEGER NOT NULL REFERENCES oauth_grants(id),
  redirect_uri TEXT NOT NULL,
  redirect_explicit INTEGER NOT NULL,
  code_challenge TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  used_at TEXT);
CREATE INDEX oauth_codes_grant ON oauth_codes(grant_id);
CREATE TABLE oauth_tokens (
  hash BLOB PRIMARY KEY,
  grant_id INTEGER NOT NULL REFERENCES oauth_grants(id),
  kind TEXT NOT NULL CHECK (kind IN ('access','refresh')),
  scopes TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  used_at TEXT,
  created_at TEXT NOT NULL);
CREATE INDEX oauth_tokens_grant ON oauth_tokens(grant_id);
