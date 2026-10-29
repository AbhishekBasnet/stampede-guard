CREATE TABLE IF NOT EXISTS devices (
    token       TEXT PRIMARY KEY,
    platform    TEXT NOT NULL CHECK (platform IN ('android', 'ios')),
    app_build   INTEGER NOT NULL,
    created_at  INTEGER NOT NULL                 -- unix milliseconds
);

CREATE TABLE IF NOT EXISTS campaigns (
    id                  TEXT PRIMARY KEY,         -- cmp_ + 12 hex
    name                TEXT NOT NULL,
    title               TEXT NOT NULL,
    body                TEXT NOT NULL,
    audience_json       TEXT NOT NULL,            -- {"kind":"all"} or {"kind":"sample","fraction":0.25}
    pacing              TEXT NOT NULL CHECK (pacing IN ('unlimited', 'static', 'adaptive')),
    static_rate         INTEGER NOT NULL DEFAULT 0,
    min_rate            INTEGER NOT NULL,
    max_rate            INTEGER NOT NULL,
    deadline_seconds    INTEGER NOT NULL,
    amplification_prior REAL NOT NULL DEFAULT 0,  -- 0 = automatic
    status              TEXT NOT NULL CHECK (status IN ('draft','running','paused','completed','expired','aborted')),
    targeted            INTEGER NOT NULL DEFAULT 0,
    sent                INTEGER NOT NULL DEFAULT 0,
    failed              INTEGER NOT NULL DEFAULT 0,
    expired             INTEGER NOT NULL DEFAULT 0,
    baseline_rps        REAL NOT NULL DEFAULT 0,  -- admitted RPS just before start
    final_amplification REAL NOT NULL DEFAULT 0,  -- measured after the campaign settles
    created_at          INTEGER NOT NULL,
    started_at          INTEGER,
    deadline_at         INTEGER,
    finished_at         INTEGER
);

-- The outbox: one row per (campaign, device). Survives restarts.
CREATE TABLE IF NOT EXISTS campaign_deliveries (
    campaign_id TEXT NOT NULL REFERENCES campaigns(id),
    token       TEXT NOT NULL,
    state       TEXT NOT NULL CHECK (state IN ('pending', 'sent', 'failed', 'expired')),
    attempts    INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT '',
    sent_at     INTEGER,
    PRIMARY KEY (campaign_id, token)
);
CREATE INDEX IF NOT EXISTS idx_deliveries_state ON campaign_deliveries (campaign_id, state);

-- Pacer timeline, one row per dispatcher tick, used for reports.
CREATE TABLE IF NOT EXISTS pacer_ticks (
    campaign_id   TEXT NOT NULL,
    at            INTEGER NOT NULL,               -- unix milliseconds
    rate          REAL NOT NULL,
    rate_cap      REAL NOT NULL,
    reason        TEXT NOT NULL,
    sent_total    INTEGER NOT NULL,
    headroom      REAL NOT NULL,
    mode          TEXT NOT NULL,
    p99_ms        REAL NOT NULL,
    amplification REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ticks_campaign ON pacer_ticks (campaign_id, at);
