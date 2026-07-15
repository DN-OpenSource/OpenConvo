-- OpenStream initial schema (SPEC.md §4.2)

CREATE TABLE apps (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name          TEXT NOT NULL,
  api_key       TEXT UNIQUE NOT NULL,
  api_secret    TEXT NOT NULL,
  settings      JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
  app_id        UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  id            TEXT NOT NULL,
  name          TEXT NOT NULL DEFAULT '',
  image         TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'user',
  teams         TEXT[] NOT NULL DEFAULT '{}',
  online        BOOLEAN NOT NULL DEFAULT false,
  invisible     BOOLEAN NOT NULL DEFAULT false,
  banned        BOOLEAN NOT NULL DEFAULT false,
  ban_expires   TIMESTAMPTZ,
  deactivated_at TIMESTAMPTZ,
  deleted_at    TIMESTAMPTZ,
  last_active   TIMESTAMPTZ,
  revoke_tokens_issued_before TIMESTAMPTZ,
  custom        JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, id)
);
CREATE INDEX users_name_idx ON users (app_id, name text_pattern_ops);
CREATE INDEX users_custom_gin ON users USING gin (custom);

CREATE TABLE channel_types (
  app_id        UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  builtin       BOOLEAN NOT NULL DEFAULT false,
  config        JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, name)
);

CREATE TABLE roles (
  app_id        UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  builtin       BOOLEAN NOT NULL DEFAULT false,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, name)
);

CREATE TABLE channels (
  app_id        UUID NOT NULL,
  type          TEXT NOT NULL,
  id            TEXT NOT NULL,
  cid           TEXT GENERATED ALWAYS AS (type || ':' || id) STORED,
  created_by    TEXT,
  team          TEXT NOT NULL DEFAULT '',
  frozen        BOOLEAN NOT NULL DEFAULT false,
  disabled      BOOLEAN NOT NULL DEFAULT false,
  cooldown      INT NOT NULL DEFAULT 0,
  member_count  INT NOT NULL DEFAULT 0,
  last_message_at TIMESTAMPTZ,
  truncated_at  TIMESTAMPTZ,
  custom        JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at    TIMESTAMPTZ,
  PRIMARY KEY (app_id, type, id)
);
CREATE INDEX channels_last_message_idx ON channels (app_id, last_message_at DESC NULLS LAST);
CREATE INDEX channels_cid_idx ON channels (app_id, cid);
CREATE INDEX channels_custom_gin ON channels USING gin (custom);

CREATE TABLE channel_members (
  app_id        UUID NOT NULL,
  channel_type  TEXT NOT NULL,
  channel_id    TEXT NOT NULL,
  user_id       TEXT NOT NULL,
  channel_role  TEXT NOT NULL DEFAULT 'channel_member',
  invited       BOOLEAN NOT NULL DEFAULT false,
  invite_accepted_at TIMESTAMPTZ,
  invite_rejected_at TIMESTAMPTZ,
  banned        BOOLEAN NOT NULL DEFAULT false,
  ban_expires   TIMESTAMPTZ,
  shadow_banned BOOLEAN NOT NULL DEFAULT false,
  hidden        BOOLEAN NOT NULL DEFAULT false,
  hide_messages_before TIMESTAMPTZ,
  pinned_at     TIMESTAMPTZ,
  archived_at   TIMESTAMPTZ,
  custom        JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, channel_type, channel_id, user_id)
);
CREATE INDEX channel_members_user_idx ON channel_members (app_id, user_id);

CREATE TABLE messages (
  app_id        UUID NOT NULL,
  id            TEXT NOT NULL,
  channel_type  TEXT NOT NULL,
  channel_id    TEXT NOT NULL,
  user_id       TEXT NOT NULL,
  text          TEXT NOT NULL DEFAULT '',
  html          TEXT NOT NULL DEFAULT '',
  type          TEXT NOT NULL DEFAULT 'regular',
  parent_id     TEXT,
  show_in_channel BOOLEAN NOT NULL DEFAULT false,
  quoted_message_id TEXT,
  reply_count   INT NOT NULL DEFAULT 0,
  reaction_counts JSONB NOT NULL DEFAULT '{}',
  reaction_scores JSONB NOT NULL DEFAULT '{}',
  mentioned_users TEXT[] NOT NULL DEFAULT '{}',
  attachments   JSONB NOT NULL DEFAULT '[]',
  silent        BOOLEAN NOT NULL DEFAULT false,
  pinned        BOOLEAN NOT NULL DEFAULT false,
  pinned_by     TEXT,
  pinned_at     TIMESTAMPTZ,
  pin_expires   TIMESTAMPTZ,
  shadowed      BOOLEAN NOT NULL DEFAULT false,
  poll_id       UUID,
  i18n          JSONB,
  custom        JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at    TIMESTAMPTZ,
  PRIMARY KEY (app_id, channel_type, channel_id, created_at, id)
) PARTITION BY RANGE (created_at);

-- Unique message id lookup support + channel pagination live on each
-- partition; a default partition guarantees writes never fail between
-- partition-maintenance runs.
CREATE TABLE messages_default PARTITION OF messages DEFAULT;
CREATE INDEX messages_channel_idx ON messages (app_id, channel_type, channel_id, created_at DESC);
CREATE INDEX messages_id_idx ON messages (app_id, id);
CREATE INDEX messages_parent_idx ON messages (app_id, parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX messages_custom_gin ON messages USING gin (custom);

-- Monthly partition maintenance (SPEC.md §4.2): called on migrate and from
-- the retention worker to keep current+next month materialized.
CREATE OR REPLACE FUNCTION ensure_message_partition(month_start date) RETURNS void AS $$
DECLARE
  part_name text := 'messages_' || to_char(month_start, 'YYYY_MM');
  from_ts timestamptz := month_start;
  to_ts   timestamptz := (month_start + interval '1 month');
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name) THEN
    EXECUTE format(
      'CREATE TABLE %I PARTITION OF messages FOR VALUES FROM (%L) TO (%L)',
      part_name, from_ts, to_ts);
  END IF;
END;
$$ LANGUAGE plpgsql;

SELECT ensure_message_partition(date_trunc('month', now())::date);
SELECT ensure_message_partition((date_trunc('month', now()) + interval '1 month')::date);

CREATE TABLE reactions (
  app_id        UUID NOT NULL,
  message_id    TEXT NOT NULL,
  user_id       TEXT NOT NULL,
  type          TEXT NOT NULL,
  score         INT NOT NULL DEFAULT 1,
  custom        JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, message_id, user_id, type)
);
CREATE INDEX reactions_message_idx ON reactions (app_id, message_id, created_at DESC);

CREATE TABLE reads (
  app_id        UUID NOT NULL,
  channel_type  TEXT NOT NULL,
  channel_id    TEXT NOT NULL,
  user_id       TEXT NOT NULL,
  last_read     TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_read_message_id TEXT NOT NULL DEFAULT '',
  unread_messages INT NOT NULL DEFAULT 0,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, channel_type, channel_id, user_id)
);
CREATE INDEX reads_user_idx ON reads (app_id, user_id);

CREATE TABLE devices (
  app_id        UUID NOT NULL,
  user_id       TEXT NOT NULL,
  id            TEXT NOT NULL,
  push_provider TEXT NOT NULL,
  push_provider_name TEXT NOT NULL DEFAULT '',
  disabled      BOOLEAN NOT NULL DEFAULT false,
  disabled_reason TEXT NOT NULL DEFAULT '',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, user_id, id)
);

CREATE TABLE bans (
  app_id        UUID NOT NULL,
  target_user_id TEXT NOT NULL,
  channel_type  TEXT NOT NULL DEFAULT '',
  channel_id    TEXT NOT NULL DEFAULT '',
  banned_by     TEXT NOT NULL DEFAULT '',
  reason        TEXT NOT NULL DEFAULT '',
  shadow        BOOLEAN NOT NULL DEFAULT false,
  expires       TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, target_user_id, channel_type, channel_id)
);
CREATE INDEX bans_channel_idx ON bans (app_id, channel_type, channel_id);

CREATE TABLE mutes (
  app_id        UUID NOT NULL,
  user_id       TEXT NOT NULL,
  target_user_id TEXT NOT NULL,
  expires       TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, user_id, target_user_id)
);

CREATE TABLE channel_mutes (
  app_id        UUID NOT NULL,
  user_id       TEXT NOT NULL,
  channel_type  TEXT NOT NULL,
  channel_id    TEXT NOT NULL,
  expires       TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, user_id, channel_type, channel_id)
);

CREATE TABLE flags (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  app_id        UUID NOT NULL,
  created_by    TEXT NOT NULL,
  target_message_id TEXT NOT NULL DEFAULT '',
  target_user_id TEXT NOT NULL DEFAULT '',
  reason        TEXT NOT NULL DEFAULT '',
  custom        JSONB NOT NULL DEFAULT '{}',
  reviewed_at   TIMESTAMPTZ,
  reviewed_by   TEXT NOT NULL DEFAULT '',
  review_result TEXT NOT NULL DEFAULT '',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX flags_unique_idx ON flags (app_id, created_by, target_message_id, target_user_id) WHERE reviewed_at IS NULL;
CREATE INDEX flags_queue_idx ON flags (app_id, created_at DESC) WHERE reviewed_at IS NULL;

CREATE TABLE blocklists (
  app_id        UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  mode          TEXT NOT NULL DEFAULT 'exact',
  behavior      TEXT NOT NULL DEFAULT 'flag',
  words         TEXT[] NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, name)
);

CREATE TABLE commands (
  app_id        UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  args          TEXT NOT NULL DEFAULT '',
  set_name      TEXT NOT NULL DEFAULT '',
  url           TEXT NOT NULL DEFAULT '',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, name)
);

CREATE TABLE webhooks (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  app_id        UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  url           TEXT NOT NULL,
  kind          TEXT NOT NULL DEFAULT 'after',  -- after | before_message_send | command
  event_types   TEXT[] NOT NULL DEFAULT '{}',
  enabled       BOOLEAN NOT NULL DEFAULT true,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE polls (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  app_id        UUID NOT NULL,
  created_by    TEXT NOT NULL,
  name          TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  voting_visibility TEXT NOT NULL DEFAULT 'public',
  enforce_unique_vote BOOLEAN NOT NULL DEFAULT true,
  max_votes_allowed INT NOT NULL DEFAULT 1,
  allow_user_suggested_options BOOLEAN NOT NULL DEFAULT false,
  is_closed     BOOLEAN NOT NULL DEFAULT false,
  custom        JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE poll_options (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  poll_id       UUID NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
  text          TEXT NOT NULL,
  custom        JSONB NOT NULL DEFAULT '{}'
);

CREATE TABLE poll_votes (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  poll_id       UUID NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
  option_id     UUID NOT NULL REFERENCES poll_options(id) ON DELETE CASCADE,
  user_id       TEXT NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (poll_id, option_id, user_id)
);

CREATE TABLE pending_messages (
  app_id        UUID NOT NULL,
  message_id    TEXT NOT NULL,
  channel_type  TEXT NOT NULL,
  channel_id    TEXT NOT NULL,
  kind          TEXT NOT NULL DEFAULT 'moderation', -- moderation | scheduled | reminder
  run_at        TIMESTAMPTZ,
  payload       JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, message_id, kind)
);

CREATE TABLE tasks (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  app_id        UUID NOT NULL,
  kind          TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'pending', -- pending|running|completed|failed
  payload       JSONB NOT NULL DEFAULT '{}',
  result        JSONB NOT NULL DEFAULT '{}',
  error         TEXT NOT NULL DEFAULT '',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX tasks_pending_idx ON tasks (created_at) WHERE status IN ('pending','running');

CREATE TABLE imports (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  app_id        UUID NOT NULL,
  task_id       UUID,
  path          TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL DEFAULT 'pending',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE exports (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  app_id        UUID NOT NULL,
  task_id       UUID,
  path          TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL DEFAULT 'pending',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Transactional outbox (SPEC.md §2.3): events are committed with the data
-- mutation and relayed to the bus by the outbox relay worker.
CREATE TABLE outbox (
  id            BIGSERIAL PRIMARY KEY,
  app_id        UUID NOT NULL,
  topic         TEXT NOT NULL,
  payload       JSONB NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  published_at  TIMESTAMPTZ
);
CREATE INDEX outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;

-- Bounded per-channel event log for /sync replay (SPEC.md §8.1, 7-day window).
CREATE TABLE event_log (
  id            BIGSERIAL PRIMARY KEY,
  app_id        UUID NOT NULL,
  cid           TEXT NOT NULL,
  user_id       TEXT NOT NULL DEFAULT '',
  event_id      TEXT NOT NULL,
  payload       JSONB NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX event_log_cid_idx ON event_log (app_id, cid, created_at);
CREATE INDEX event_log_prune_idx ON event_log (created_at);

-- Moderation audit log (SPEC.md §11.6).
CREATE TABLE audit_log (
  id            BIGSERIAL PRIMARY KEY,
  app_id        UUID NOT NULL,
  actor_id      TEXT NOT NULL,
  action        TEXT NOT NULL,
  target_type   TEXT NOT NULL DEFAULT '',
  target_id     TEXT NOT NULL DEFAULT '',
  reason        TEXT NOT NULL DEFAULT '',
  detail        JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_app_idx ON audit_log (app_id, created_at DESC);
