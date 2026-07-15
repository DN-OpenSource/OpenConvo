# OpenStream Chat — Complete Build Prompt Playbook

Sequenced prompts for implementing the entire OpenStream Chat spec with Claude Code (or any coding agent). **AI features are excluded (v2 scope).**

**How to use this playbook:**

1. Run prompts **in order** — each builds on the previous. Don't skip.
2. Keep `SPEC.md` (OpenStream-Chat-Spec.md) in the repo root; every prompt references it.
3. Each prompt ends with **acceptance criteria** — do not move on until they pass.
4. After every 3–4 prompts, run the **Checkpoint Prompt** (Section F) to catch drift.
5. Recommended `CLAUDE.md` for the repo is in Appendix A — set it up before Prompt 1.

---

## A. Foundation (Prompts 1–6)

### Prompt 1 — Repository scaffold & CLAUDE.md

```
Read SPEC.md sections 2, 3, and 23. Scaffold the OpenStream monorepo exactly per the
repository structure in section 23:
- Go module github.com/openstream/openstream, Go 1.26
- cmd/openstream with cobra CLI: subcommands `serve` (all-in-one), `api`, `realtime`,
  `worker`, `gateway`, `migrate`, `doctor` — all stubs that start and log
- internal/ packages: api, realtime, domain, store, bus, search, storage, push,
  moderation, webhook, worker — each with a doc.go describing its responsibility
  (copy responsibility text from SPEC.md)
- Config loading: YAML file + OPENSTREAM_* env override + flags (precedence:
  flags > env > yaml), with a typed Config struct covering: http addr, ws addr,
  postgres dsn, redis addr, nats url, storage (s3 endpoint/bucket/creds),
  search (meilisearch url/key), log level
- Makefile: build, test, lint (golangci-lint), fmt, migrate-up, migrate-down, dev
- docker-compose.dev.yml: postgres:17, redis:7.4 (or valkey:8), nats:2.11 with
  JetStream, meilisearch:v1.15, minio — with healthchecks
- GitHub Actions CI: lint + test + build on push
- .golangci.yml with sensible strict config
Acceptance: `make build` succeeds, `docker compose -f docker-compose.dev.yml up -d`
all healthy, `go run ./cmd/openstream serve` starts and logs config, `make lint` clean.
```

### Prompt 2 — Database schema & migrations

```
Read SPEC.md section 4 (Core Domain Model) fully. Implement the complete PostgreSQL
schema with golang-migrate migrations in internal/store/migrations:
- All tables from 4.2: apps, users, channels, channel_members, messages (range
  partitioned by created_at, monthly; write a partition-maintenance function +
  worker job stub), attachments, reactions, reads, devices, bans, mutes, flags,
  blocklists, commands, webhooks, polls, poll_options, poll_votes,
  pending_messages, imports, exports, channel_types, roles, permission_grants,
  outbox (transactional outbox: id, topic, payload jsonb, created_at, published_at)
- Indexes: channels(app_id, last_message_at desc), messages(app_id, channel_type,
  channel_id, created_at desc), GIN on all custom jsonb columns, reads lookup,
  outbox(published_at) partial where published_at is null
- FKs where cheap; skip FK on messages->channels (partition perf), enforce in code
- Seed migration: 5 built-in channel types with the exact default configs from
  SPEC.md section 6.2, built-in roles from section 7.1
Then configure sqlc (sqlc.yaml) and write queries for: apps CRUD, users upsert
batch/partial/query, channel create-get/update/delete, members add/remove/query,
message insert/update/soft-delete/list-paginated (id_lt/id_gt/around_id),
reactions add/remove/list, reads upsert + unread counts aggregate, outbox
insert/claim-batch/mark-published.
Acceptance: `make migrate-up` on fresh dev DB succeeds and is idempotent;
`sqlc generate` clean; `make migrate-down` fully reverts; add a store integration
test using testcontainers-go proving message insert + paginate + partition works.
```

### Prompt 3 — Domain layer: entities, channel types, permission engine

```
Read SPEC.md sections 4, 6, 7. In internal/domain implement:
- Entity structs (App, User, Channel, Member, Message, Reaction, Attachment, Read,
  Device, ChannelType, Role) with json tags matching Stream-style API field names
  (cid, custom fields flattened via custom MarshalJSON/UnmarshalJSON: unknown JSON
  keys go into a Custom map[string]any; reserved keys rejected on input, max 5KB)
- ChannelType config struct with every flag from section 6.1 and defaults per 6.2
- Permission engine: resources/actions constants (full ~60 list from 7.1),
  Grant{Role, Action, Owner bool, Allow bool}, policy evaluation
  (most-specific-first, deny overrides), and ComputeOwnCapabilities(user, channel,
  channelType) returning the capability string list
- Channel ID rules: validate type/id charset, distinct-channel ID derivation =
  "!members-" + base64(sha256(sorted member ids))
- CID parse/format helpers
Write exhaustive table-driven unit tests for the permission engine (admin vs user
vs guest vs channel_moderator vs owner across 15+ actions) and for custom-field
marshaling round-trips.
Acceptance: go test ./internal/domain/... passes with >90% coverage on the
permission engine; lint clean.
```

### Prompt 4 — Auth: JWT tokens, middleware, app resolution

```
Read SPEC.md section 10. In internal/api/auth implement:
- JWT verify (HS256 with app secret; structure ready for RS256/EdDSA later):
  claims user_id, exp/iat optional, server bool
- Token minting helpers (used by CLI + tests): user token, server token, guest token
- HTTP middleware chain: resolve app by api_key (query or X-Api-Key header) ->
  verify Authorization JWT -> load/auto-create user context -> inject into
  request context. Server tokens may act on behalf of another user via user_id param.
- Development mode: disable_auth_checks app setting accepts unsigned dev tokens
- Token revocation: revoke_tokens_issued_before at app and user level (check iat)
- Guest endpoint POST /api/v1/guest -> creates guest user + token (role guest)
- CLI: `openstream token --app <key> --user <id> [--server] [--exp 24h]`
Acceptance: unit tests cover valid/expired/revoked/wrong-secret/server-on-behalf
cases; integration test hits a protected stub endpoint with each token class;
lint clean.
```

### Prompt 5 — Event bus, transactional outbox & Redis state

```
Read SPEC.md sections 2.3 and 8.3. Implement:
- internal/bus: Bus interface (Publish(topic, event), Subscribe(pattern) with
  queue-group support) + NATS JetStream implementation + in-process implementation
  (for single-binary mode and tests). Subjects: evt.{app}.{channelType}.{channelID}
  and usr.{app}.{userID}. Event envelope: type, cid, user, payload, created_at,
  event_id (ULID).
- Outbox relay worker: poll/claim outbox rows (FOR UPDATE SKIP LOCKED, batch 100),
  publish to bus, mark published; metrics for lag.
- internal/store/redis: presence registry (SADD conn per user, TTL heartbeat),
  typing state (SETEX per user per channel, 8s TTL), unread counter cache
  (write-through, invalidate on read-state change), rate limit token buckets
  (Lua script), watcher counts per channel.
Acceptance: integration test — insert outbox row in a tx with a message, relay
publishes to NATS, subscriber receives within 500ms; Redis presence and typing
TTL expiry tests; in-proc bus passes the same interface test suite as NATS.
```

### Prompt 6 — HTTP server skeleton, error model, rate limiting

```
Read SPEC.md section 9.3. Build the chi-based HTTP server in internal/api:
- Router mounted at /api/v1 with middleware: request ID, real IP, structured
  logging (slog), recover, OTel tracing, CORS per app settings, auth (Prompt 4),
  rate limiting (Redis token buckets: per-app server-side 5000/min, per-user
  60 writes/min, configurable; emit X-RateLimit-Limit/Remaining/Reset headers)
- Stream-compatible error envelope {code, message, status_code, more_info} with a
  numeric code registry in internal/api/apierror (start with: 4 input error,
  5 not allowed, 16 does not exist, 17 not owner, 9 rate limited, 40-43 token
  errors, 60 internal); helper apierror.Write(w, err)
- Health endpoints /healthz /readyz; Prometheus /metrics
- OpenAPI: create api/openapi.yaml skeleton (info, servers, auth schemes, error
  schema) — every subsequent prompt appends its endpoints to this file
Acceptance: server boots via `openstream api`; a stub authed endpoint returns the
error envelope correctly for each failure class; rate-limit integration test
observes 429 + headers; /metrics exposes http histograms.
```

---

## B. Core Chat API (Prompts 7–14)

### Prompt 7 — Users API

```
Read SPEC.md 5.3 (U1-U3, U8-U10, U16) and section 9. Implement in internal/api:
- POST /users (batch upsert ≤100), PATCH /users (partial set/unset)
- POST /users/query with the Mongo-style filter compiler: build a reusable
  internal/store/filters package translating {$eq,$ne,$gt,$gte,$lt,$lte,$in,$nin,
  $autocomplete,$exists,$and,$or,$nor,$contains} into parameterized SQL over
  typed columns + JSONB custom fields (whitelist approach, no injection possible)
- Deactivate/reactivate/delete users (soft + hard; hard delete cascades messages
  per mode user|messages|conversations as async task rows; task runner stub in
  worker; GET /tasks/{id})
- Block/unblock/list-blocked user endpoints
- Teams enforcement hooks per section 7.2 (filter injected when multi_tenant)
Update api/openapi.yaml. Write filter-compiler unit tests (20+ cases incl.
malicious input) and integration tests for upsert/query/partial.
Acceptance: all tests green; filter compiler fuzz test (go-fuzz seed corpus) runs
clean for 30s; openapi.yaml validates with a linter.
```

### Prompt 8 — Channels API (CRUD, query, membership)

```
Read SPEC.md 5.2 (C1-C5, C8, C10, C11, C13) and section 9.1. Implement:
- POST /channels/{type}/{id}/query — create-or-get; supports member-list creation
  (distinct channels via domain helper), state=true returns channel + members +
  messages page + reads + own_capabilities; watch/presence flags parsed (wired to
  realtime in Prompt 12)
- POST /channels/{type}/query (distinct by members, no id)
- POST /channels (query channels): filter compiler reuse over type/cid/members/
  last_message_at/custom; sort whitelist (last_message_at, created_at, member_count,
  unread_count has_unread via reads join); pagination limit/offset; options
  message_limit, member_limit
- Update full POST + partial PATCH (set/unset custom, frozen, cooldown, team);
  member ops in update payload: add_members, remove_members, invites,
  add_moderators, demote_moderators, hide_history, plus accept_invite/reject_invite
- DELETE (soft; ?hard_delete=true server-only), hide/show (with clear_history),
  mute/unmute channel, stop-watching
- Every mutation: permission check via domain engine + channel-type config check +
  outbox event emission (channel.created/updated/deleted/hidden/visible,
  member.added/removed/updated, notification.* variants per section 8.2)
Update openapi.yaml. Integration tests: distinct channel resolves same cid for
same member set; frozen channel rejects sends; hidden channel excluded from query
until new message unhides.
Acceptance: tests green; every mutation verified to write exactly one outbox event
(table-driven event assertion test).
```

### Prompt 9 — Messages API (send, edit, delete, threads, pagination)

```
Read SPEC.md 5.1 (M1-M6, M11, M19) and 9.1. Implement:
- POST /channels/{type}/{id}/message: validation (max_message_length from channel
  type, reserved fields), idempotency by client message id, mentions extraction,
  markdown -> sanitized HTML (goldmark + bluemonday), silent flag, thread rules
  (parent_id must exist, no nested threads, show_in_channel), quoted_message_id
  validation, cooldown/slow-mode enforcement (SkipSlowMode capability),
  channel.last_message_at bump (skip for system when configured), reply_count
  increment, reads/unread bump for members, outbox: message.new +
  notification.message_new fan-out rows
- GET /messages/{id}, POST /messages/{id} (update), PUT partial, DELETE
  (soft tombstone type=deleted; ?hard=true), GET /messages/{id}/replies
  (thread pagination), GET /channels/.../messages?ids=
- Pagination on channel query state: id_lt/id_lte/id_gt/id_gte/around_id and
  created_at_* variants
- GET /unread summary endpoint (total, per channel, threads)
- Mark read/unread endpoints (channel-level + from-message) updating reads +
  emitting message.read / notification.mark_read|mark_unread
Update openapi.yaml. Integration tests: idempotent double-send, thread reply
counts, unread lifecycle across two users, pagination window correctness,
soft vs hard delete visibility.
Acceptance: tests green; p95 send latency < 30ms in the integration environment
(add a small benchmark test); event assertions for every mutation.
```

### Prompt 10 — Reactions, pins & message actions

```
Read SPEC.md 5.1 (M7, M10) . Implement:
- POST /messages/{id}/reaction (type required, custom data, score support,
  enforce_unique option), DELETE /messages/{id}/reaction/{type},
  GET /messages/{id}/reactions (paginated)
- Denormalized reaction_counts/reaction_scores maintained transactionally on the
  message row; latest reactions (10) embedded in message payloads
- Pin/unpin via message update (pinned, pin_expires); pinned messages query
  endpoint; pin expiry sweep in worker
- Permission checks: CreateReaction, DeleteOwnReaction/DeleteAnyReaction, PinMessage
- Events: reaction.new/updated/deleted, message.updated on pin
Update openapi.yaml. Tests: unique reaction replacement, score accumulation
(clap x5), counts consistency under 50 concurrent reactors (race test with -race).
Acceptance: tests green including the concurrency test; no lost updates.
```

### Prompt 11 — Bans, mutes, flags & blocklists (moderation core)

```
Read SPEC.md section 11 (items 1, 3, 4, 6 — NOTE: AI classifiers are v2; implement
blocklist + webhook classifier hook only) and 5.4. Implement:
- POST/DELETE /moderation/ban (global + channel, timed, shadow), query banned
- POST/DELETE /moderation/mute (user, timed) + channel mute done in Prompt 8
- Shadow ban semantics: shadowed users' messages get shadowed=true, visible only
  to author (filter in reads paths + realtime delivery)
- POST /moderation/flag + unflag (message/user, reason, custom), review queue
  GET /moderation/queue with filters, review actions (approve/delete/ban)
- Blocklists CRUD (/blocklists) with modes exact/wildcard/regex and behaviors
  block/flag/shadow_block; ship default profanity list as embedded asset
- Automod middleware chain in internal/moderation: blocklist matcher -> optional
  webhook classifier (POST message to configured URL, 200ms timeout,
  fail-open/closed configurable) -> decision allow/flag/block/shadow; wired into
  message create/update
- Audit log table + write on every moderation action; query endpoint (server-only)
- Slash commands /ban /mute /unban /unmute /flag mapped to these APIs
Update openapi.yaml. Tests: timed ban expiry, shadow message invisibility to
others but visibility to author, blocklist regex behavior matrix, webhook
classifier timeout fail-open.
Acceptance: tests green; audit log rows asserted for each action.
```

### Prompt 12 — Realtime WebSocket engine

```
Read SPEC.md section 8 fully. Implement internal/realtime:
- WS endpoint /connect per 8.1 (gobwas/ws): auth via query params, health.check
  hello with connection_id + own_user (unreads, mutes, devices), ping/pong 25s,
  dead-eviction 60s
- Connection registry: connID -> user, watched channel set; per-connection
  outbound buffered queue with write coalescing; slow-consumer disconnect with
  connection.slow close code
- Interest-based NATS subscriptions: subscribe evt.{app}.{cid} on first local
  watcher, unsubscribe on last; always subscribe usr.{app}.{userID} per connected
  user
- Watch/stop-watching wired from channel query API (Prompt 8): API service
  registers watch intent via Redis + realtime picks it up (or same-process direct
  call in serve mode)
- Presence: Redis conn registry + user.presence.changed events with 10s offline
  debounce; watcher counts; invisible mode
- Typing: channel event endpoint publishes typing.start/stop through Redis
  pub/sub (not persisted), auto-expiry emits typing.stop
- /sync endpoint: replay per-channel events since last_sync_at from a bounded
  event log table (7-day retention, retention sweep in worker)
- Livestream broadcast tier per 8.3 for channels with >5k watchers
Tests: two-client integration test (send -> both receive message.new < 500ms),
reconnect + /sync replay, presence transitions, typing expiry, slow-consumer drop
(client that stops reading), watcher count accuracy.
Acceptance: all realtime integration tests green with -race; 1k-connection smoke
test in CI (lightweight swarm) stays under 512MB RSS.
```

### Prompt 13 — File uploads & storage

```
Read SPEC.md section 15 (skip virus-scan adapter wiring beyond interface). Implement:
- internal/storage: Storage interface + S3/MinIO adapter + local-disk adapter
- POST /channels/{type}/{id}/image and /file (multipart ≤100MB default), DELETE
  same; permission UploadAttachment; MIME sniffing + per-app allow/deny lists
- Image pipeline in worker: EXIF strip, thumbnails (libvips via bimg or pure-Go
  fallback), blurhash; attachment rows updated async, message.updated event on
  completion
- Signed time-limited download URLs; public-CDN mode toggle
- Attachment size/count limits per channel type
Update openapi.yaml. Tests: upload->attach->send message flow, oversized reject,
MIME spoof reject, signed URL expiry, thumbnail job produces variants (use a
tiny fixture png).
Acceptance: tests green against MinIO in compose; local-disk adapter passes the
same interface test suite.
```

### Prompt 14 — Search

```
Read SPEC.md section 14 (skip semantic/vector — v2). Implement:
- internal/search: SearchBackend interface + Meilisearch adapter + Postgres FTS
  fallback adapter; async indexing consumer on message.new/updated/deleted events;
  reindex CLI command
- POST /search: query + Mongo-style filters (cid $in, created_at ranges,
  mentioned_users), sort relevance|recency, cursor pagination; permission-aware:
  pre-filter to channels the requester can read (and team-scoped)
- Index config: message text, attachment titles, user name, channel name;
  configurable custom fields
Update openapi.yaml. Tests: index-then-search roundtrip (eventually-consistent
with retry), permission leak test (user B cannot find user A's private channel
messages), deletion removes from index; FTS adapter passes same suite.
Acceptance: both adapters green on the shared conformance suite.
```

---

## C. Platform Features (Prompts 15–20)

### Prompt 15 — Webhooks & custom commands

```
Read SPEC.md section 13 (skip AI bot example — v2). Implement:
- Webhook config CRUD (server-only): url, event subscription list, enabled
- After-event delivery workers: consume bus, filter per subscription, POST signed
  (X-Signature HMAC-SHA256 of raw body), retries expo backoff max 6, dead-letter
  table + endpoint to inspect/requeue
- Before-message-send synchronous hook: 200ms budget, response can mutate or
  reject the message; fail-open/closed per config
- Custom commands: /commands CRUD, command detection in message create, POST to
  handler URL, apply response (message mutation or ephemeral message M12 —
  implement ephemeral message type here)
- SSRF protection: resolve + deny private ranges, re-check on redirect (shared
  helper also used by URL enrichment later)
- Queue sink adapters: NATS subject re-publish + generic interface for
  Kafka/SQS/RabbitMQ (implement NATS only, interface + docs for the rest)
Tests: delivery retry/dead-letter path with a flaky test server, signature
verification, before-hook mutation + rejection + timeout fail modes, SSRF denial
matrix, /giphy-style command roundtrip with ephemeral then commit-on-action
(implement POST /messages/{id}/action).
Acceptance: tests green; webhook delivery metrics exposed.
```

### Prompt 16 — Push notifications

```
Read SPEC.md section 12. Implement:
- Device endpoints (register/list/remove) with provider field (firebase, apn, webpush)
- Push provider config per app (multiple named providers), stored encrypted
- Worker consumers on message.new/notification events applying delivery rules:
  offline-only default (configurable), skip muted (user+channel), skip silent,
  dedupe multi-device, collapse keys per channel
- FCM v1 HTTP adapter, APNs token-based adapter, Web Push VAPID adapter — behind
  a PushProvider interface with a mock for tests
- Handlebars-style templates per event type per provider ({{sender.name}},
  {{message.text}}, {{channel.name}}); defaults + per-app overrides
- POST /check_push test endpoint returning rendered payloads per device
- Invalid-token feedback handling: remove dead devices
Tests: rule matrix (online/offline x muted x silent), template rendering,
provider mock delivery assertions, dead-token pruning.
Acceptance: tests green; check_push returns correct rendered payloads.
```

### Prompt 17 — URL enrichment, truncate, invites, slow mode polish

```
Read SPEC.md M9, C6, C12, C15, U5-U7. Implement remaining P2 items:
- URL enrichment worker: on message.new with URLs (channel-type flag on), fetch
  with SSRF guard, parse OpenGraph/oEmbed, attach preview card via message.updated
- Truncate channel endpoint (hard/soft, optional system message, skip_push)
- Invite flow completion: invited member state, accept/reject endpoints +
  notification.invited / invite_accepted / invite_rejected events
- Slow mode full enforcement + SkipSlowMode capability respected
- Guest + anonymous connection paths on WS (read-only livestream)
- Invisible mode toggle endpoint
Tests per feature incl. enrichment against a local fixture server, truncate wipes
+ read-state reset, invite lifecycle events.
Acceptance: tests green; feature flags from channel-type config all respected
(matrix test over the 6.1 flag list for a custom channel type).
```

### Prompt 18 — Channel-type, roles & app-config admin APIs

```
Read SPEC.md sections 6, 7, and app settings from 9.1 Config block. Implement
server-only admin APIs:
- /channeltypes CRUD honoring every flag in 6.1, 50-type limit, permission
  policies embedded; changing flags takes effect without restart (config cache
  with bus invalidation)
- /roles CRUD (custom roles), /permissions grant editing per channel type
- GET/PATCH /app (settings: multi_tenant, disable_auth_checks, upload lists,
  webhook config pointers, push config pointers)
- CLI parity: `openstream app create|list`, `channeltype get|update`
Tests: custom channel type end-to-end (create type with reactions=false ->
reaction attempt on channel of that type rejected), permission grant change
takes effect live, 50-type limit.
Acceptance: tests green; own_capabilities reflects custom grants correctly.
```

### Prompt 19 — Import / export & async tasks

```
Read SPEC.md section 21. Implement:
- Generic async task framework (tasks table already exists): states pending/
  running/completed/failed, GET /tasks/{id}, webhook completion event
- POST /imports accepting Stream Chat export JSON (users, channels, members,
  messages, reactions, devices): validation endpoint returning row-level errors,
  then batched idempotent import preserving timestamps and message ids
- POST /export_channels (filters, JSON/CSV) + user data export (GDPR) -> signed
  S3 URL results
- Retention worker: message_retention channel-type setting enforcement
Tests: golden-file import of a realistic Stream export fixture (create one, ~200
messages, threads + reactions), re-import idempotency, export->import roundtrip
equality, GDPR export contains all user rows.
Acceptance: roundtrip test proves lossless import/export for supported fields.
```

### Prompt 20 — Dashboard (admin UI)

```
Read SPEC.md 3.3 and section 25 (frontend versions). Build dashboard/ (React 19.2
+ TS + Vite, embedded via embed.FS behind /dashboard with server-token session):
Pages: app overview + API keys, channel-type editor (all 6.1 flags + permission
matrix grid editor per 7.1), user explorer (query/ban/deactivate), channel
explorer with message view + moderation actions, moderation review queue,
blocklist editor, webhook config + delivery log/dead-letter viewer, push provider
config + push tester UI (calls /check_push), live event inspector (WS tail),
import/export runner with task progress.
Keep styling minimal and clean (Tailwind); use TanStack Query; no AI features.
Acceptance: `make build` embeds dashboard; manual smoke checklist in
dashboard/SMOKE.md covering each page against the compose stack; Playwright e2e
for: login, create channel type, edit permission grant, ban user from queue.
```

---

## D. SDKs & Examples (Prompts 21–25)

### Prompt 21 — JS/TS core client SDK (@openstream/chat)

```
Read SPEC.md 16.1, 16.2 and sections 8-9. In sdk/js create a pnpm workspace.
Package @openstream/chat (isomorphic: browser + Node; Node also gets server-mode
with api_secret token minting):
- Typed API client generated from api/openapi.yaml (openapi-typescript) + a
  hand-written ergonomic layer mirroring Stream's client surface:
  connectUser, channel(type,id), channel.watch/query/sendMessage/sendReaction/
  markRead, queryChannels, queryUsers, search, upload, moderation methods, admin
  methods in server mode
- WS manager: connect, heartbeat, reconnect with backoff + /sync recovery,
  event emitter (typed event map from section 8.2)
- Reactive state store: channels map with ordered list (last_message_at sort,
  pinned first), per-channel state (messages, members, watchers, reads, typing),
  own-user unread totals — updated by the full event-handler matrix (the
  chat-list behaviors: reorder on message.new/notification.message_new, unread
  badge updates, preview updates on edit/delete, add/remove channel on
  notification.added_to/removed_from_channel, presence dots, typing)
- Offline: pluggable storage interface + IndexedDB implementation (channels +
  last N messages), optimistic send with retry queue
Tests: vitest unit tests for the state store event matrix (every event type ->
expected state mutation, 30+ cases), integration tests against the compose stack
(spin server in CI), reconnection/sync test.
Acceptance: npm pack works; integration suite green; state-store event matrix
test is exhaustive over section 8.2 channel events.
```

### Prompt 22 — React UI kit (@openstream/chat-react)

```
Read SPEC.md 16.1 and section 25.2 (React Compiler assumption). Build the React
kit on the JS core:
- Components: Chat (provider), ChannelList (live: sorting, unread badges, preview,
  typing line, presence dot, section for pinned), Channel, MessageList
  (virtualized, day separators, grouping, thread open), Message (reactions UI,
  attachments incl. image gallery + file cards + link preview card, quoted reply,
  pinned indicator, edit/delete menus per own_capabilities), MessageInput
  (mentions autocomplete, attachments upload with progress, slash command
  autocomplete, typing events, edit mode), Thread, ChannelHeader, Avatar,
  TypingIndicator, UnreadBadge
- Theming: CSS variables, light/dark, RTL; i18n via a messages dict (en + hi
  starter); WCAG 2.1 AA pass on interactive elements
- Built with React Compiler enabled; React 18.3 fallback build
- Storybook for every component; example app examples/react-messenger
Tests: RTL component tests for ChannelList live behaviors (inject events ->
assert reorder/badge/preview), MessageInput mention flow; Playwright e2e on the
example app against compose: two browsers exchange messages, reactions, threads,
typing, read receipts.
Acceptance: storybook builds; e2e green in CI; a11y axe checks pass on core
components.
```

### Prompt 23 — Go & Node server SDKs

```
Read SPEC.md 16.2. Implement:
- chat-go (separate module dir sdk/go): typed client over openapi (oapi-codegen)
  + ergonomic layer: token minting, users upsert, channel create/update, send
  message server-side, moderation, channel-type admin, webhooks verify helper
  (signature check), import/export helpers. Used by the server's own e2e tests.
- Node server mode: extend @openstream/chat server entrypoint with token minting
  (jsonwebtoken), webhook signature verify, admin namespaces
Docs: sdk/go/README + sdk/js/README with quickstarts (mint token -> connect ->
send).
Acceptance: server e2e test suite is rewritten to consume chat-go (dogfooding);
Node server-mode integration test mints token used by a browser test.
```

### Prompt 24 — Example apps & compat façade

```
Read SPEC.md 16.3 and examples list in 23. Build:
- examples/slack-clone (React kit: team channel type, threads-first, sidebar)
- examples/livestream (livestream type, anonymous viewers, watcher count)
- examples/support (commerce type, two-role flow)
- --stream-compat flag: a translation middleware mapping the most common Stream
  Chat v1 REST paths/payload shapes onto our handlers (connect, query channels,
  send message, reactions, users upsert); document covered surface + gaps in
  docs/stream-compat.md
Acceptance: each example runs against compose with a seed script; compat façade
passes a smoke script exercising the documented surface.
```

### Prompt 25 — P3 features batch (no AI)

```
Read SPEC.md M15-M18, M20-M21, C16, and campaigns in 5.5. Implement in order:
polls (full CRUD + vote events + React components), scheduled messages +
reminders (worker schedules), drafts (server-synced), message translation
(pluggable provider, LibreTranslate adapter, i18n field), edit history option,
pinned/archived channels per member + query support, campaigns/broadcast API
(server-only, batched sends as tasks). NOTE: skip all AI/LLM message features —
v2. Update openapi.yaml, JS SDK, React kit, tests per feature as in prior prompts.
Acceptance: per-feature integration tests green; poll e2e in React example.
```

---

## E. Hardening & Ship (Prompts 26–29)

### Prompt 26 — Load testing & performance

```
Read SPEC.md section 17. Build loadtest/: k6 REST scenarios (send/query mix) +
Go WS swarm (connect N, watch channels, measure send->deliver latency
distribution). Targets: single node 10k conns / 1k msg/s sustained, p99 deliver
< 250ms. Profile (pprof) and fix the top 3 hotspots found. Document methodology +
results in docs/benchmarks.md. Add nightly CI perf job with regression thresholds.
Acceptance: targets met on the reference dev machine or documented gap analysis
with issues filed; perf CI job runs green.
```

### Prompt 27 — Security pass

```
Read SPEC.md section 20. Execute a security checklist and fix findings:
gosec + govulncheck clean; SSRF matrix re-test (enrichment, webhooks, commands);
upload MIME/extension bypass attempts; JSONB filter compiler injection fuzz
(extend corpus); authz matrix test — for every endpoint in openapi.yaml assert
the unauthenticated, wrong-user, wrong-team, and guest cases; rate-limit
bypass attempts; secrets-at-rest verification; CORS config test; dependency
lockfile provenance (npm provenance + gosum verify in CI). Add SECURITY.md
disclosure policy, cosign release signing + SBOM (syft) to the release workflow.
Acceptance: all scanners clean or triaged in SECURITY-TRIAGE.md; authz matrix
test covers 100% of routes (enforced by a route-coverage assertion).
```

### Prompt 28 — Deployment artifacts

```
Read SPEC.md section 18. Produce: production docker-compose.yml (all services +
backups note), Helm chart (api/realtime/worker deployments, HPA on realtime by
connection-count custom metric, PDBs, NetworkPolicies, values.yaml documented),
single-binary mode (SQLite + in-proc bus + local disk; document 'not for
production scale'), `openstream doctor`, zero-downtime notes (WS drain +
connection.recovered), Grafana dashboards + alert rules in deploy/observability.
Acceptance: compose prod boots clean on a fresh VM script; helm template lints
(kubeconform); kind-based CI job installs the chart and passes a smoke test;
doctor detects a stopped redis.
```

### Prompt 29 — Docs site & release

```
Read SPEC.md sections 9, 16, 22. Build docs/ (Docusaurus): quickstart (compose ->
first message in 5 min), REST reference generated from openapi.yaml, WS events
reference from section 8.2, SDK guides (JS, React, Go, Node), self-hosting +
Helm guide, migration-from-Stream guide (import + compat façade + dual-write
recipe), permission cookbook, push setup per provider. CI executes every code
sample against the compose stack (docs freshness rule from spec 25.3).
Cut v0.1.0: changelog, signed artifacts, ghcr images.
Acceptance: docs build; sample-execution CI green; release workflow produces
signed multi-arch images.
```

---

## F. Checkpoint Prompt (run after every 3–4 prompts)

```
Audit the repo against SPEC.md for everything implemented so far:
1. Run make lint test and the full integration suite; fix all failures.
2. Diff implemented endpoints vs api/openapi.yaml vs SPEC.md section 9 — list and
   fix any drift (missing endpoints, undocumented params, wrong error codes).
3. Verify every mutation endpoint emits exactly the events SPEC.md section 8.2
   specifies (extend the event assertion table test).
4. Verify no AI-scope features (SPEC.md section 24 v2 list) have leaked into code,
   API, or docs.
5. Check test coverage: fail if internal/domain <90% or overall <75%.
6. Update MEMORY.md with architecture decisions made since last checkpoint.
Produce a short CHECKPOINT.md report: what passed, what was fixed, open risks.
```

---

## Appendix A — Recommended repo CLAUDE.md

```markdown
# OpenStream Chat — agent conventions
- SPEC.md is the source of truth; when code and spec conflict, flag it, don't guess.
- AI/LLM features are v2 (SPEC.md §24) — never implement, stub, or document them.
- Every API mutation must: check permissions (internal/domain), respect
  channel-type flags (§6.1), write a transactional outbox event (§8.2), and be
  covered by an integration test asserting the emitted event.
- Errors always use internal/api/apierror envelope; never fmt.Errorf to clients.
- DB access only through sqlc queries; new queries need a migration review.
- Keep api/openapi.yaml in sync in the same PR as any endpoint change.
- Tests: table-driven; integration tests use testcontainers; always run with -race.
- Run `make lint test` before declaring any task complete.
```

---

*Playbook v1.0 — pairs with OpenStream-Chat-Spec.md (Spec v1.1, AI features moved to v2).*
