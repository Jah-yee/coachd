CREATE TABLE activities (
    id            INTEGER PRIMARY KEY,
    start_local   TEXT NOT NULL,
    start_utc     TEXT NOT NULL,
    date          TEXT NOT NULL,
    week_start    TEXT NOT NULL,
    type_key      TEXT NOT NULL DEFAULT '',
    sport         TEXT NOT NULL DEFAULT 'other',
    name_untrusted TEXT NOT NULL DEFAULT '',
    distance_m    REAL NOT NULL DEFAULT 0,
    duration_s    REAL NOT NULL DEFAULT 0,
    moving_s      REAL NOT NULL DEFAULT 0,
    elev_gain_m   REAL NOT NULL DEFAULT 0,
    elev_loss_m   REAL NOT NULL DEFAULT 0,
    avg_hr        REAL,
    max_hr        REAL,
    manual        INTEGER NOT NULL DEFAULT 0,
    training_load REAL,
    aerobic_te    REAL,
    anaerobic_te  REAL,
    vo2max        REAL,
    tss           REAL,
    fit_path      TEXT NOT NULL DEFAULT '',
    fit_status    TEXT NOT NULL DEFAULT 'pending',
    fit_attempts  INTEGER NOT NULL DEFAULT 0,
    raw_json      BLOB
);
CREATE INDEX idx_activities_date ON activities(date);

CREATE TABLE daily_health (
    date                 TEXT PRIMARY KEY,
    sleep_s              INTEGER,
    hrv_overnight_ms     INTEGER,
    hrv_weekly_avg_ms    INTEGER,
    resting_hr           INTEGER,
    spo2_avg             INTEGER,
    weight_kg            REAL,
    sleep_score          INTEGER,
    hrv_status           TEXT,
    hrv_baseline_low_ms  INTEGER,
    hrv_baseline_high_ms INTEGER,
    body_battery_max     INTEGER,
    body_battery_min     INTEGER,
    body_battery_charged INTEGER,
    body_battery_drained INTEGER,
    stress_avg           INTEGER
);

CREATE TABLE day_metrics (
    date             TEXT PRIMARY KEY,
    hrv_7d_ms        REAL,
    hrv_band_low_ms  REAL,
    hrv_band_high_ms REAL,
    hrv_position     TEXT NOT NULL DEFAULT '',
    resting_hr_7d    REAL,
    resting_hr_60d   REAL,
    sleep_7d_s       REAL,
    longest_run_7d_m  REAL NOT NULL DEFAULT 0,
    longest_run_7d_s  REAL NOT NULL DEFAULT 0,
    longest_run_30d_m REAL NOT NULL DEFAULT 0,
    longest_run_30d_s REAL NOT NULL DEFAULT 0,
    longest_run_90d_m REAL NOT NULL DEFAULT 0,
    longest_run_90d_s REAL NOT NULL DEFAULT 0
);

CREATE TABLE raw_payloads (
    source     TEXT NOT NULL,
    key        TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    json       BLOB NOT NULL,
    PRIMARY KEY (source, key)
);

CREATE TABLE sync_state (
    data_type        TEXT PRIMARY KEY,
    last_attempt_at  TEXT,
    last_success_at  TEXT,
    freshest_data_at TEXT,
    last_outcome     TEXT NOT NULL DEFAULT '',
    cursor           TEXT NOT NULL DEFAULT '',
    backfill_done    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    hash         BLOB NOT NULL UNIQUE,
    scopes       TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    last_used_at TEXT,
    revoked_at   TEXT
);

CREATE TABLE audit_log (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    at      TEXT NOT NULL,
    lane    TEXT NOT NULL DEFAULT '',
    actor   TEXT NOT NULL DEFAULT '',
    client  TEXT NOT NULL DEFAULT '',
    event   TEXT NOT NULL DEFAULT '',
    target  TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_audit_log_at ON audit_log(at);
