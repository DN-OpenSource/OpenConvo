# OpenStream Chat

Open-source, self-hostable chat infrastructure — a Stream Chat–compatible
server written in Go. Single binary for development, horizontally scalable
with PostgreSQL + Redis + NATS in production.

The full product specification lives in [SPEC.md](SPEC.md); the build
playbook in [doc/OpenStream-Build-Prompts.md](doc/OpenStream-Build-Prompts.md).
This repository currently implements the **P1 core** (see *Status* below).

## Quick start

```bash
# 1. Start infrastructure (Postgres, Redis/Valkey, NATS, Meilisearch, MinIO)
make dev

# 2. Migrate, create an app, mint a token
make build
./bin/openstream migrate up
./bin/openstream app create my-app        # prints api_key + api_secret
./bin/openstream token --app <api_key> --user alice

# 3. Run everything in one process
./bin/openstream serve
```

First message:

```bash
TOKEN=$(./bin/openstream token --app $API_KEY --user alice)

# create-or-get a channel with members
curl -X POST "localhost:3030/api/v1/channels/messaging/general/query?api_key=$API_KEY" \
  -H "Authorization: $TOKEN" \
  -d '{"data":{"members":["bob"]},"state":true,"watch":true}'

# send a message
curl -X POST "localhost:3030/api/v1/channels/messaging/general/message?api_key=$API_KEY" \
  -H "Authorization: $TOKEN" \
  -d '{"message":{"text":"hello **world**"}}'
```

Realtime WebSocket:

```
ws://localhost:3030/connect?api_key=<api_key>&authorization=<jwt>
```

The first frame is a `health.check` event carrying your `connection_id` and
`me` (own user with unread counts, mutes, devices). Pass the
`connection_id` to channel query with `"watch": true` to receive
`message.new`, `typing.start`, reactions and membership events live.

## Architecture

A modular monolith (SPEC.md §2): one Go binary with subcommands —
`serve` (all-in-one), `api`, `realtime`, `worker` — deployable as separate
processes at scale.

- **PostgreSQL** is the source of truth (messages range-partitioned by
  month; JSONB custom fields on every entity).
- **Transactional outbox**: every mutation commits its events atomically
  with the data; a relay worker publishes them to the bus (at-least-once,
  nothing lost between commit and publish).
- **NATS** fans events out across nodes (in-process bus in single-binary
  mode); realtime nodes hold interest-based subscriptions per channel.
- **Redis/Valkey** holds presence, watcher counts and rate-limit windows
  (in-memory fallback in single-binary mode).
- **Permissions v2**: per-channel-type grant policies, owner-scoped rules,
  deny-overrides, computed `own_capabilities` on every channel payload.

```
cmd/openstream/        CLI + service entrypoints
internal/domain/       entities, channel types, permission engine, CID rules
internal/store/        pgx store, migrations, outbox, filter compiler
internal/api/          REST handlers, auth middleware, error envelope
internal/realtime/     WS engine, connection registry, presence
internal/bus/          NATS + in-process event bus
internal/state/        Redis + in-memory ephemeral state
internal/moderation/   blocklists, automod pipeline, webhook classifier
internal/worker/       outbox relay, retention sweeps
internal/integration/  full-stack integration tests (build tag `integration`)
```

## Status

Implemented (P1 core, SPEC.md §22):

- Auth: HS256 JWTs (user/server/guest), on-behalf-of, revocation
  watermarks, dev-token mode, guest endpoint
- Users: batch upsert, partial set/unset, Mongo-style query filters
  (whitelisted compiler — `$eq $ne $gt $gte $lt $lte $in $nin
  $autocomplete $exists $contains $and $or $nor`, JSONB custom fields),
  deactivate/reactivate/delete, devices
- Channels: create-or-get, distinct channels (member-set identity), query
  channels with filters + sort, full/partial update, member ops
  (add/remove/invite/moderators), freeze, hide/show, mute, truncate,
  soft/hard delete, `own_capabilities`
- Messages: send (idempotent client ids), sanitized markdown HTML, threads
  with reply counts, quoted replies, silent/system messages, pagination
  (`id_lt/id_gt/id_around/created_at_*`), soft/hard delete, pins,
  slow mode, teams isolation
- Reactions: scores (cumulative), enforce-unique, transactional
  denormalized counts (advisory-locked — no lost updates under concurrency)
- Read state: per-member unread counts, mark read/unread-from-message,
  `/unread` summary, unread in WS hello
- Moderation: global/channel bans (timed), shadow bans (author-only
  visibility end-to-end incl. WS delivery), mutes, flags + review queue,
  blocklists (exact/wildcard/regex × flag/block/shadow), webhook
  classifier hook (fail-open/closed), audit log
- Realtime: `/connect` WS, health.check hello, interest-based
  subscriptions, watch/stop-watching + watcher counts, typing events
  (ephemeral), presence with offline debounce, slow-consumer disconnect,
  `/sync` replay (7-day event log)
- Platform: multi-app clusters, per-app channel-type CRUD (all §6.1
  flags), blocklist CRUD, app settings, rate limiting with
  `X-RateLimit-*` headers, Stream-compatible error envelope, Prometheus
  `/metrics`, structured logging
- Ops: Docker Compose dev stack, CLI (`migrate`, `app`, `token`,
  `doctor`), CI (lint + unit + integration)

Not yet implemented (P2/P3 per the roadmap): search backends, file
uploads/storage adapters, push notifications, webhooks delivery workers,
URL enrichment, slash commands, polls, scheduled messages, translation,
import/export, the dashboard UI, client SDKs, and the Stream-compat
façade. AI features are v2 scope and intentionally absent (SPEC.md §24).

Known deviations from the playbook, made deliberately:

- Hand-written parameterized pgx queries instead of sqlc codegen (no sqlc
  toolchain dependency; the store package is the single SQL boundary).
- Permission grants are stored inside each channel type's config JSONB
  rather than a separate `permission_grants` table.
- `notification.message_new` fan-out is bounded to channels with ≤100
  members; larger channels rely on channel-scoped delivery (push workers
  will cover offline members in P2).

## Development

```bash
make build          # build ./bin/openstream
make test           # unit tests (-race)
make dev            # start compose dependencies
make test-integration  # full-stack tests against Postgres
make lint           # golangci-lint
```

Integration tests run against `OPENSTREAM_TEST_POSTGRES_DSN` and skip when
it is unset. See [CLAUDE.md](CLAUDE.md) for repository conventions.

## License

Apache 2.0
