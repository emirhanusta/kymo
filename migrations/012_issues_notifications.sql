-- 012_issues_notifications.sql
-- Server-side issues (one open row per workload + type) and notification
-- channels / delivery log. Plain tables: they are small and updated in place.

CREATE TABLE IF NOT EXISTS issues (
    id               BIGSERIAL PRIMARY KEY,
    source_name      TEXT        NOT NULL,
    workload_id      TEXT        NOT NULL,
    namespace        TEXT        NOT NULL DEFAULT '',
    kind             TEXT        NOT NULL DEFAULT '',
    name             TEXT        NOT NULL DEFAULT '',
    type             TEXT        NOT NULL,
    severity         TEXT        NOT NULL,
    title            TEXT        NOT NULL,
    detail           TEXT        NOT NULL DEFAULT '',
    status           TEXT        NOT NULL DEFAULT 'open',
    first_seen       TIMESTAMPTZ NOT NULL,
    last_seen        TIMESTAMPTZ NOT NULL,
    resolved_at      TIMESTAMPTZ,
    last_notified_at TIMESTAMPTZ,
    notify_count     INT         NOT NULL DEFAULT 0,
    flaps            INT         NOT NULL DEFAULT 0
);

-- At most one open issue per (workload, type).
CREATE UNIQUE INDEX IF NOT EXISTS uq_issues_open
    ON issues (workload_id, type) WHERE status = 'open';
CREATE INDEX IF NOT EXISTS idx_issues_status_first
    ON issues (status, first_seen DESC);
CREATE INDEX IF NOT EXISTS idx_issues_workload
    ON issues (workload_id, first_seen DESC);
CREATE INDEX IF NOT EXISTS idx_issues_source_status
    ON issues (source_name, status);
CREATE INDEX IF NOT EXISTS idx_issues_resolved_at
    ON issues (resolved_at DESC) WHERE status = 'resolved';

CREATE TABLE IF NOT EXISTS notification_channels (
    id              BIGSERIAL PRIMARY KEY,
    name            TEXT        NOT NULL,
    type            TEXT        NOT NULL CHECK (type IN ('slack', 'discord', 'webhook')),
    -- 'user' channels are created through the API (url_enc / secret_enc are
    -- AES-GCM sealed); 'config' channels mirror config.yaml (the file holds the
    -- URL in plain text, so url_enc / secret_enc hold the plain bytes).
    origin          TEXT        NOT NULL DEFAULT 'user',
    url_enc         BYTEA       NOT NULL,
    secret_enc      BYTEA,
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,
    min_severity    TEXT        NOT NULL DEFAULT 'critical' CHECK (min_severity IN ('critical', 'warning')),
    sources         TEXT[]      NOT NULL DEFAULT '{}',
    namespaces      TEXT[]      NOT NULL DEFAULT '{}',
    types           TEXT[]      NOT NULL DEFAULT '{}',
    notify_resolved BOOLEAN     NOT NULL DEFAULT TRUE,
    include_logs    BOOLEAN     NOT NULL DEFAULT TRUE,
    repeat_minutes  INT         NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_notification_channels_config_name
    ON notification_channels (name) WHERE origin = 'config';

CREATE TABLE IF NOT EXISTS notification_deliveries (
    id          BIGSERIAL PRIMARY KEY,
    channel_id  BIGINT      NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
    issue_id    BIGINT,
    event       TEXT        NOT NULL CHECK (event IN ('opened', 'resolved', 'reminder', 'test')),
    ok          BOOLEAN     NOT NULL,
    status_code INT         NOT NULL DEFAULT 0,
    error       TEXT        NOT NULL DEFAULT '',
    attempts    INT         NOT NULL DEFAULT 1,
    sent_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_notification_deliveries_channel
    ON notification_deliveries (channel_id, sent_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_issue
    ON notification_deliveries (issue_id, channel_id, sent_at DESC) WHERE issue_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_sent
    ON notification_deliveries (sent_at);
