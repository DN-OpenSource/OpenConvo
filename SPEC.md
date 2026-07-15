# OpenStream Chat — Open-Source Stream Chat Replica

**Full Feature & Technical Specification Document**

| | |
|---|---|
| **Project codename** | OpenStream Chat (working name — final name TBD) |
| **Goal** | 100% feature-parity, open-source, self-hostable replica of Stream Chat (getstream.io/chat) |
| **License** | Apache 2.0 (permissive — usable by anyone, including commercial) |
| **Backend language** | Go (Golang) |
| **Version** | Spec v1.1 — July 2026 (v1.1: AI features deferred to v2; framework currency section added) |

---

## Table of Contents

1. [Vision & Goals](#1-vision--goals)
2. [System Architecture](#2-system-architecture)
3. [Technology Stack](#3-technology-stack)
4. [Core Domain Model](#4-core-domain-model)
5. [Feature Specification (Full Parity List)](#5-feature-specification)
6. [Channel Types & Configuration](#6-channel-types--configuration)
7. [Permissions, Roles & Multi-Tenancy](#7-permissions-roles--multi-tenancy)
8. [Real-Time Engine (WebSocket & Events)](#8-real-time-engine)
9. [REST API Specification](#9-rest-api-specification)
10. [Authentication & Tokens](#10-authentication--tokens)
11. [Moderation Suite](#11-moderation-suite)
12. [Push Notifications](#12-push-notifications)
13. [Webhooks & Integrations](#13-webhooks--integrations)
14. [Search](#14-search)
15. [File & Media Handling](#15-file--media-handling)
16. [SDK Matrix (Backend + Frontend)](#16-sdk-matrix)
17. [Scalability & Performance Targets](#17-scalability--performance-targets)
18. [Deployment & DevOps](#18-deployment--devops)
19. [Observability](#19-observability)
20. [Security & Compliance](#20-security--compliance)
21. [Data Import / Export & Migration](#21-data-import--export--migration)
22. [Roadmap & Release Phases](#22-roadmap--release-phases)
23. [Repository Structure](#23-repository-structure)
24. [Non-Goals (v1) & v2 Scope](#24-non-goals-v1--v2-scope)
25. [Framework Currency & Version Targets](#25-framework-currency--version-targets)

---

## 1. Vision & Goals

### 1.1 Vision

Build the **"Supabase of chat"** — a fully open-source, self-hostable, horizontally scalable chat infrastructure platform that any developer can run with a single `docker compose up`, with SDKs for every major frontend and backend framework, and a feature set matching Stream Chat's commercial offering.

### 1.2 Design Principles

1. **API-compatible mindset** — API shapes closely mirror Stream Chat's concepts (channels, channel types, watchers, members, events) so developers familiar with Stream feel at home, and migration from Stream is near drop-in.
2. **Single-binary simplicity, cluster-grade scale** — one Go binary runs everything for dev/small deployments; the same binary scales out horizontally with Postgres + Redis + NATS for production.
3. **Batteries included** — moderation, push, search, file storage, and a dashboard ship in the box. No paid tiers, no feature gates.
4. **SDK-first** — the server is only half the product. First-class client SDKs + UI component kits for React, React Native, Flutter, iOS (Swift), Android (Kotlin/Compose), Angular, Vue, plus server SDKs for Go, Node, Python, PHP, Java, .NET, Ruby.
5. **Extensible** — custom fields on every entity (users, channels, messages, reactions, attachments), custom events, custom commands, webhook hooks at every lifecycle point.

### 1.3 Success Criteria

- Feature-parity checklist (Section 5) ≥ 95% complete at v1.0 GA.
- Single node handles 10k concurrent WebSocket connections and 1k msg/sec sustained.
- Cluster of 5 nodes handles 250k+ concurrent connections.
- p99 message send-to-deliver latency < 250 ms intra-region.
- `docker compose up` to first message in < 5 minutes.

---

## 2. System Architecture

### 2.1 High-Level Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                          CLIENT LAYER                            │
│  React / RN / Flutter / iOS / Android / Angular / Vue / Unity    │
│        (UI Kits → State/Offline Layer → Low-Level Client)        │
└───────────────┬────────────────────────────┬─────────────────────┘
                │ HTTPS (REST)               │ WSS (WebSocket)
                ▼                            ▼
┌──────────────────────────────────────────────────────────────────┐
│                    EDGE / GATEWAY (Go)                           │
│   TLS termination · Rate limiting · Auth (JWT) · Routing        │
└───────────────┬────────────────────────────┬─────────────────────┘
                ▼                            ▼
┌───────────────────────────┐  ┌──────────────────────────────────┐
│   API SERVICE (Go)        │  │   REALTIME SERVICE (Go)          │
│  - Channels/Messages CRUD │  │  - WS connection registry        │
│  - Users/Members/Roles    │  │  - Presence & typing             │
│  - Reactions/Threads      │  │  - Event fan-out                 │
│  - Search/Moderation      │  │  - Watcher management            │
└──────┬──────────┬─────────┘  └───────────┬──────────────────────┘
       │          │                        │
       │          └────────┐   ┌───────────┘
       ▼                   ▼   ▼
┌────────────┐      ┌──────────────┐      ┌───────────────────────┐
│ PostgreSQL │      │  NATS JetStream │   │  Redis                │
│ (source of │      │  (event bus,    │   │  (presence, typing,   │
│  truth)    │      │   fan-out,      │   │   rate limits, cache, │
│            │      │   webhooks)     │   │   unread counters)    │
└────────────┘      └──────────────┘      └───────────────────────┘
       │
       ▼
┌────────────┐   ┌──────────────┐   ┌─────────────┐   ┌──────────┐
│ S3 / MinIO │   │ Meilisearch/ │   │ Push Workers│   │ Webhook  │
│ (files/CDN)│   │ OpenSearch   │   │ (FCM/APNs)  │   │ Workers  │
└────────────┘   └──────────────┘   └─────────────┘   └──────────┘
```

### 2.2 Service Decomposition

The system is a **modular monolith** in Go compiled to a single binary with subcommands, deployable as separate processes at scale:

| Service | Binary command | Responsibility |
|---|---|---|
| **gateway** | `openstream gateway` | TLS, auth verification, rate limiting, request routing |
| **api** | `openstream api` | All REST endpoints, business logic, persistence |
| **realtime** | `openstream realtime` | WebSocket termination, event delivery, presence |
| **worker** | `openstream worker` | Push notifications, webhooks, URL enrichment, thumbnails, retention jobs, scheduled exports |
| **all-in-one** | `openstream serve` | All of the above in one process (dev / small deployments) |

**Rationale for modular monolith over microservices:** single repo, single deploy artifact, shared domain types, dramatically lower ops burden for self-hosters — while process-level separation still allows independent scaling of the WebSocket tier (memory-bound) vs API tier (CPU/DB-bound).

### 2.3 Event Flow (message send)

1. Client POSTs message → API service.
2. API validates permissions, runs moderation pipeline (automod/blocklists), persists to Postgres (transactional outbox pattern).
3. Outbox relay publishes `message.new` to NATS subject `app.{app_id}.channel.{cid}`.
4. Every realtime node subscribed to that subject delivers the event to local watchers' WebSockets.
5. Worker consumers on the same event: unread counters (Redis), push notifications for offline members, webhook delivery, search indexing.

The **transactional outbox** guarantees no event is lost between DB commit and bus publish.

---

## 3. Technology Stack

### 3.1 Backend (Go)

| Concern | Choice | Notes |
|---|---|---|
| Language | **Go 1.26+** (see §25 for version policy) | Same language Stream uses; excellent concurrency for WS fan-out; Green Tea GC (default in 1.26) reduces GC overhead 10–40% — significant for event fan-out workloads |
| HTTP framework | **chi** (or stdlib `net/http` + `http.ServeMux`) | Lightweight, middleware-friendly, no magic |
| WebSocket | **gobwas/ws** or **coder/websocket** | gobwas for epoll-based low-memory connections at scale |
| Database | **PostgreSQL 16+** | Source of truth; JSONB for custom fields; partitioning for messages |
| DB access | **pgx v5** + **sqlc** | Type-safe generated queries, no heavy ORM |
| Migrations | **golang-migrate** | Versioned SQL migrations |
| Event bus | **NATS JetStream** | Lightweight, single-binary, at-least-once delivery, ideal for self-hosting (Kafka optional adapter for large scale) |
| Cache / ephemeral state | **Redis 7+** (or Valkey) | Presence, typing TTLs, unread counts, rate limits, hot channel cache |
| Search | **Meilisearch** (default) / **OpenSearch** (adapter) | Message & user search; pluggable interface |
| Object storage | **S3-compatible** (AWS S3, MinIO, GCS via adapter) | Attachments, avatars, exports |
| Auth tokens | **JWT (HS256 default, RS256/EdDSA supported)** | Same model as Stream: server-side SDKs mint user tokens |
| Push | **FCM v1** + **APNs (token-based)** + **Web Push (VAPID)** | Provider config per app |
| Config | **YAML + env vars** (`OPENSTREAM_*`) | 12-factor |
| Observability | **OpenTelemetry** (traces/metrics) + **Prometheus** + structured logs (slog) | |
| Testing | Go test + testcontainers-go | Full integration suite against real Postgres/Redis/NATS |

### 3.2 Infrastructure Defaults

- **Docker Compose** for dev and small production.
- **Helm chart** for Kubernetes.
- **Terraform modules** (AWS/GCP/Azure) as community-maintained extras.

### 3.3 Dashboard (Admin UI)

- **React + TypeScript + Vite**, served by the Go binary (embedded via `embed.FS`).
- App management, channel-type editor, permission matrix editor, user explorer, message moderation queue, webhook config, push config, API key management, live event log, usage analytics.

---

## 4. Core Domain Model

### 4.1 Entity Relationship Overview

```
App 1──* ChannelType 1──* Channel 1──* Message 1──* Reaction
 │                          │  │          │
 │                          │  │          ├──* Attachment
 │                          │  │          ├──* Thread replies (self-ref parent_id)
 │                          │  │          └──1 Poll (optional)
 ├──* User *──────* Member ─┘  │
 │        └──* Device          └──* Read (per member read state)
 ├──* Role / Permission Policy
 ├──* Webhook
 ├──* Command (custom slash commands)
 ├──* BlockList
 └──* PushProvider
```

### 4.2 Tables (PostgreSQL) — Key Schemas

**apps** — multi-app support on one cluster (like Stream's app concept)

```sql
CREATE TABLE apps (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name          TEXT NOT NULL,
  api_key       TEXT UNIQUE NOT NULL,
  api_secret    TEXT NOT NULL,            -- encrypted at rest
  settings      JSONB NOT NULL DEFAULT '{}',  -- automod defaults, multi_tenant flag, etc.
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**users**

```sql
CREATE TABLE users (
  app_id        UUID NOT NULL REFERENCES apps(id),
  id            TEXT NOT NULL,             -- developer-provided ID
  name          TEXT,
  image         TEXT,
  role          TEXT NOT NULL DEFAULT 'user',
  teams         TEXT[] NOT NULL DEFAULT '{}',   -- multi-tenancy
  online        BOOLEAN NOT NULL DEFAULT false, -- denormalized presence snapshot
  invisible     BOOLEAN NOT NULL DEFAULT false,
  banned        BOOLEAN NOT NULL DEFAULT false,
  ban_expires   TIMESTAMPTZ,
  deactivated_at TIMESTAMPTZ,
  deleted_at    TIMESTAMPTZ,
  last_active   TIMESTAMPTZ,
  custom        JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, id)
);
```

**channels**

```sql
CREATE TABLE channels (
  app_id        UUID NOT NULL,
  type          TEXT NOT NULL,             -- messaging | livestream | team | gaming | commerce | custom
  id            TEXT NOT NULL,
  cid           TEXT GENERATED ALWAYS AS (type || ':' || id) STORED,
  created_by    TEXT,
  team          TEXT,                      -- multi-tenancy
  frozen        BOOLEAN NOT NULL DEFAULT false,
  disabled      BOOLEAN NOT NULL DEFAULT false,
  hidden        BOOLEAN NOT NULL DEFAULT false, -- per-user hidden handled in members
  cooldown      INT NOT NULL DEFAULT 0,    -- slow mode seconds
  member_count  INT NOT NULL DEFAULT 0,
  last_message_at TIMESTAMPTZ,
  truncated_at  TIMESTAMPTZ,
  custom        JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at    TIMESTAMPTZ,
  PRIMARY KEY (app_id, type, id)
);
```

**channel_members**

```sql
CREATE TABLE channel_members (
  app_id        UUID NOT NULL,
  channel_type  TEXT NOT NULL,
  channel_id    TEXT NOT NULL,
  user_id       TEXT NOT NULL,
  channel_role  TEXT NOT NULL DEFAULT 'channel_member',  -- channel_member | channel_moderator | owner
  invited       BOOLEAN NOT NULL DEFAULT false,
  invite_accepted_at TIMESTAMPTZ,
  invite_rejected_at TIMESTAMPTZ,
  banned        BOOLEAN NOT NULL DEFAULT false,
  ban_expires   TIMESTAMPTZ,
  shadow_banned BOOLEAN NOT NULL DEFAULT false,
  muted         BOOLEAN NOT NULL DEFAULT false,
  hidden        BOOLEAN NOT NULL DEFAULT false,
  pinned_at     TIMESTAMPTZ,               -- pinned channels per user
  archived_at   TIMESTAMPTZ,               -- archived channels per user
  custom        JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, channel_type, channel_id, user_id)
);
```

**messages** (range-partitioned by created_at month)

```sql
CREATE TABLE messages (
  app_id        UUID NOT NULL,
  id            TEXT NOT NULL,             -- client-generatable UUID (idempotency)
  channel_type  TEXT NOT NULL,
  channel_id    TEXT NOT NULL,
  user_id       TEXT NOT NULL,
  text          TEXT NOT NULL DEFAULT '',
  html          TEXT,                      -- rendered markdown (server-side, sanitized)
  type          TEXT NOT NULL DEFAULT 'regular', -- regular|reply|system|ephemeral|error|deleted
  parent_id     TEXT,                      -- thread parent
  show_in_channel BOOLEAN NOT NULL DEFAULT false,
  quoted_message_id TEXT,
  reply_count   INT NOT NULL DEFAULT 0,
  reaction_counts JSONB NOT NULL DEFAULT '{}',
  reaction_scores JSONB NOT NULL DEFAULT '{}',
  mentioned_users TEXT[] NOT NULL DEFAULT '{}',
  silent        BOOLEAN NOT NULL DEFAULT false,
  pinned        BOOLEAN NOT NULL DEFAULT false,
  pinned_by     TEXT,
  pinned_at     TIMESTAMPTZ,
  pin_expires   TIMESTAMPTZ,
  shadowed      BOOLEAN NOT NULL DEFAULT false,
  poll_id       UUID,
  i18n          JSONB,                     -- translations
  custom        JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at    TIMESTAMPTZ,
  PRIMARY KEY (app_id, channel_type, channel_id, created_at, id)
) PARTITION BY RANGE (created_at);
```

Supporting tables: `attachments`, `reactions`, `reads` (per-member last_read + unread counts), `devices` (push tokens), `bans`, `mutes`, `flags` (moderation reports), `blocklists`, `commands`, `webhooks`, `polls`, `poll_options`, `poll_votes`, `pending_messages` (reminders/scheduled), `imports`, `exports`, `channel_types`, `roles`, `permission_grants`.

### 4.3 ID & Custom-Data Conventions

- All entity IDs are **developer-provided strings** (like Stream), enabling idempotent upserts and easy sync with an existing user DB.
- `cid` = `"{type}:{id}"` is the universal channel address used across the API and events.
- Every entity accepts arbitrary **custom JSON fields** (reserved keys are rejected). Max custom-data size: 5 KB per entity (configurable).

---

## 5. Feature Specification

The complete Stream Chat parity checklist. Every feature below is in scope. Phase column maps to the roadmap in Section 22.

### 5.1 Messaging Core

| # | Feature | Description | Phase |
|---|---|---|---|
| M1 | Send/update/delete messages | Full CRUD; soft delete (tombstone) + hard delete (server-side) | P1 |
| M2 | Message types | `regular`, `reply`, `system`, `ephemeral`, `error`, `deleted` | P1 |
| M3 | Markdown rendering | Server renders sanitized HTML; clients render natively | P1 |
| M4 | @Mentions | `mentioned_users` resolution, mention events, mention push | P1 |
| M5 | Threads & replies | `parent_id` threading, `show_in_channel`, reply counts, thread participants | P1 |
| M6 | Quoted replies | `quoted_message_id` inline quoting | P1 |
| M7 | Reactions | Add/remove; custom reaction types; custom data on reactions; **reaction scores** (cumulative, e.g. claps) ; enforce-unique option; reaction pagination | P1 |
| M8 | Attachments | Images, videos, audio, files; multiple per message; custom attachment types (location, product card, etc.) | P1 |
| M9 | URL enrichment / link previews | Async OpenGraph scraping worker; preview cards with title/description/image; per-channel-type toggle | P2 |
| M10 | Pinned messages | Pin/unpin with optional expiry; query pinned messages endpoint | P2 |
| M11 | Silent messages | Persisted but no unread count / push (system updates) | P1 |
| M12 | Ephemeral messages | Visible only to sender (command previews, e.g. /giphy shuffle) | P2 |
| M13 | Message search | Full-text across accessible channels, with filters | P2 |
| M14 | Slash commands | Built-ins (`/giphy`, `/ban`, `/mute`, `/unban`, `/unmute`, `/flag`) + **custom commands** backed by webhook handlers | P2 |
| M15 | Message translation | i18n field; pluggable translation provider (LibreTranslate default adapter, DeepL/Google optional) | P3 |
| M16 | Scheduled messages | Send at future timestamp | P3 |
| M17 | Message reminders | "Remind me later" per user per message | P3 |
| M18 | Polls | Create polls in messages: options, multiple votes, anonymous, suggestions, close poll, vote events | P3 |
| M19 | Idempotent sends | Client-generated message IDs; duplicate ID = no-op | P1 |
| M20 | Rich message editing history | `message.updated` events; optional edit-history storage | P2 |
| M21 | Draft messages | Server-synced per-user per-channel drafts | P3 |
| M22 | AI/LLM message support | **Moved to v2** — streaming assistant messages, `ai_indicator` states, stop-generation event (see Section 24, v2 scope) | v2 |

### 5.2 Channels

| # | Feature | Description | Phase |
|---|---|---|---|
| C1 | Channel CRUD | Create (by ID or by member list for DMs), update (full + partial), delete (soft/hard) | P1 |
| C2 | Built-in channel types | `messaging`, `livestream`, `team`, `gaming`, `commerce` with Stream-identical default configs | P1 |
| C3 | Custom channel types | Up to 50 (configurable) per app; full feature-flag config (Section 6) | P1 |
| C4 | Distinct channels (DMs/groups) | Channel identity derived from member set — same members always resolve to the same conversation | P1 |
| C5 | Members management | Add/remove members, member roles, member custom data, hide history for new members | P1 |
| C6 | Invites | Invite / accept / reject flow with events | P2 |
| C7 | Watching | Watch/stop-watching for real-time updates; watcher counts; watcher pagination | P1 |
| C8 | Query channels | Rich filter syntax (MongoDB-style: `$eq,$ne,$gt,$gte,$lt,$lte,$in,$nin,$autocomplete,$exists,$contains`), sort, pagination, member/message/watcher inclusion | P1 |
| C9 | Channel pagination | Message history pagination (id_lt / id_gt / around_id, created_at variants) | P1 |
| C10 | Mute / unmute channel | Per-user channel mutes with optional expiry | P1 |
| C11 | Hide / show channel | Per-user hiding, optional clear-history-on-hide, auto-unhide on new message | P1 |
| C12 | Truncate channel | Wipe messages, optional system message, optional skip-push, hard/soft | P2 |
| C13 | Freeze / unfreeze | Read-only channel state (frozen) | P1 |
| C14 | Disable channel | Fully block interaction | P2 |
| C15 | Slow mode / cooldown | Per-channel message interval enforcement (skip for mods/admins) | P2 |
| C16 | Pinned & archived channels | Per-member pin/archive with query support | P3 |
| C17 | Channel capabilities | Server computes `own_capabilities` per user per channel (send-message, send-reaction, upload-file, etc.) so UIs can render permission-aware | P2 |

### 5.3 Users, Presence & Devices

| # | Feature | Description | Phase |
|---|---|---|---|
| U1 | User upsert (single/batch) | Create-or-update with custom data | P1 |
| U2 | Partial user update | `set` / `unset` semantics | P1 |
| U3 | Query users | Filter/sort/paginate; autocomplete on name | P1 |
| U4 | Presence | Online/offline via WS connection registry; `user.presence.changed` events; presence in query results | P1 |
| U5 | Invisible mode | Appear offline while connected | P2 |
| U6 | Guest users | Limited-permission ephemeral users, server-issued guest tokens | P2 |
| U7 | Anonymous users | Read-only livestream viewers, no identity | P2 |
| U8 | Deactivate / reactivate users | Single + batch with async task support | P2 |
| U9 | Delete users | Soft/hard, with cascading message deletion options (`user`, `messages`, `conversations` modes); async task + GDPR-compliant hard wipe | P2 |
| U10 | User blocking | User-level blocks (block/unblock/list blocked) — blocks DMs from blocked users | P2 |
| U11 | Devices | Register/list/remove push devices per provider | P2 |
| U12 | Multi-device / multi-tab sessions | Same user, many concurrent connections, all receiving events | P1 |
| U13 | Typing indicators | `typing.start` / `typing.stop` with server-side auto-expiry (Redis TTL) | P1 |
| U14 | Read state & receipts | Per-member `last_read`, unread counts per channel + total; `message.read` events; mark-read / mark-unread (from a message) | P1 |
| U15 | Unread counts API | Aggregated unread (channels, threads, total) at connect time and via endpoint | P1 |
| U16 | User teams | `teams[]` on users for multi-tenant isolation | P2 |

### 5.4 Moderation (full suite — Section 11 for details)

Ban / shadow ban (global + channel, timed), mute users (timed), flag message/user, moderation queue & review API, blocklists (word lists: block/flag/shadow behavior), webhook-based custom classifier hook, user-level block lists, channel freeze, message-level moderation actions (delete + notify), audit log.

### 5.5 Engagement & UX Features

Typing events, read receipts, presence, unread badges, link previews, giphy command, emoji reactions with scores, threads, pins, polls, message reminders, campaign/broadcast API (P3: send to many channels/users), channel invites, push templates.

### 5.6 Platform Features

Multi-app clusters, multi-tenancy (teams), custom roles & permission policies v2, custom events (channel-scoped + user-scoped), webhooks (before/after hooks + SQS/SNS-style queue adapters), data import (Stream-compatible JSON import format!), data export (channels/users, async tasks), rate limiting (per app/user/endpoint with headers), OpenAPI 3.1 spec published, GDPR endpoints.


---

## 6. Channel Types & Configuration

Channel types are the central configuration mechanism (identical model to Stream). Each type carries feature flags, automod config, permissions, and commands.

### 6.1 Configurable Flags (per channel type)

| Flag | Type | Default (messaging) | Meaning |
|---|---|---|---|
| `typing_events` | bool | true | Emit typing start/stop |
| `read_events` | bool | true | Track & emit read state |
| `connect_events` | bool | true | Emit watching start/stop |
| `custom_events` | bool | true | Allow custom channel events |
| `reactions` | bool | true | Allow reactions |
| `replies` | bool | true | Allow threads |
| `quotes` | bool | true | Allow quoted replies |
| `search` | bool | true | Index messages for search |
| `mutes` | bool | true | Allow channel mutes |
| `uploads` | bool | true | Allow file/image uploads |
| `url_enrichment` | bool | true | Link preview scraping |
| `push_notifications` | bool | true | Send push for this type |
| `message_retention` | string | `infinite` | `infinite` or duration (e.g. `720h`) — retention worker prunes |
| `max_message_length` | int | 5000 | Character limit |
| `automod` | enum | `disabled` | `disabled` / `simple` (blocklist) — `AI` classifier value reserved for v2 |
| `automod_behavior` | enum | `flag` | `flag` / `block` / `shadow_block` |
| `blocklist` | string | — | Named blocklist attached |
| `commands` | []string | `[all]` | Enabled slash commands |
| `mark_messages_pending` | bool | false | Hold messages for pre-approval (moderation queue) |
| `polls` | bool | false | Allow polls |
| `skip_last_msg_update_for_system_msgs` | bool | false | System msgs don't bump channel ordering |

### 6.2 Built-in Type Defaults (parity with Stream)

- **messaging** — everything on; automod `simple` available; designed for WhatsApp/Messenger-style apps.
- **livestream** — url_enrichment off, read_events off by default, permissive send for any authenticated user, anonymous read.
- **team** — Slack-style; invites-only membership semantics, threads emphasized.
- **gaming** — high-throughput; read_events off, uploads off by default.
- **commerce** — buyer/seller support chat; only members created by ops/admin can initiate.

---

## 7. Permissions, Roles & Multi-Tenancy

### 7.1 Model (Permissions v2 parity)

- **Global roles**: `user`, `guest`, `anonymous`, `admin`, `moderator` + **custom roles** (unlimited, app-defined).
- **Channel roles**: `channel_member`, `channel_moderator`, `owner` (creator).
- **Permission policies**: each channel type holds a list of grants: `(role, resource, action) → allow/deny`, evaluated most-specific-first, with `deny` overriding.
- **Resources/actions** (~60, matching Stream): `CreateChannel`, `ReadChannel`, `UpdateChannel`, `UpdateChannelMembers`, `DeleteChannel`, `CreateMessage`, `UpdateOwnMessage`, `UpdateAnyMessage`, `DeleteOwnMessage`, `DeleteAnyMessage`, `PinMessage`, `CreateReaction`, `DeleteOwnReaction`, `DeleteAnyReaction`, `UploadAttachment`, `DeleteOwnAttachment`, `DeleteAnyAttachment`, `UseCommands`, `SendCustomEvent`, `SendLinks`, `SkipSlowMode`, `BanUser`, `MuteUser`, `FlagMessage`, `ReadMessageFlags`, `RunMessageAction`, `TruncateChannel`, `FreezeChannel`, `SendPoll`, `CastPollVote`, …
- **`own_capabilities`** computed and returned with every channel payload so clients render UI without duplicating permission logic.
- Dashboard ships a **permission matrix editor** (role × action grid per channel type).

### 7.2 Multi-Tenancy (Teams)

- App-level `multi_tenant_enabled` flag.
- Users carry `teams[]`; channels carry a single `team`.
- Enforcement: users can only query/join/see channels in their teams; user queries scoped to shared teams; permission checks include team match. Server-side (secret-key) calls can bypass with explicit override.
- Use case: one deployment serving many customer workspaces (Slack-style orgs) without data leakage.

---

## 8. Real-Time Engine

### 8.1 Connection Protocol

- **WSS endpoint**: `wss://host/connect?api_key=...&stream-auth-type=jwt&authorization=<JWT>&payload=<user+device json>`
- On connect: server returns `health.check` event containing `connection_id`, full `own_user` (with unread counts, mutes, devices).
- **Heartbeat**: client/server ping-pong every 25s; server evicts dead connections at 60s; presence marked offline after last connection for the user drops (with 10s debounce for reconnects).
- **Reconnection & event recovery**: clients reconnect with backoff; `/sync` endpoint replays missed events per channel since `last_sync_at` (backed by a bounded per-channel event log in Postgres/NATS JetStream, 7-day window) — powering offline-first SDKs.
- **SSE + long-poll fallback** for restricted networks (P3).

### 8.2 Event Catalog (parity)

Channel-scoped: `message.new`, `message.updated`, `message.deleted`, `message.read`, `message.undeleted`, `reaction.new`, `reaction.updated`, `reaction.deleted`, `typing.start`, `typing.stop`, `member.added`, `member.updated`, `member.removed`, `channel.created`, `channel.updated`, `channel.deleted`, `channel.truncated`, `channel.frozen`, `channel.unfrozen`, `channel.hidden`, `channel.visible`, `user.watching.start`, `user.watching.stop`, `poll.updated`, `poll.closed`, `poll.vote_casted`, `poll.vote_changed`, `poll.vote_removed`, custom events.

User-scoped (delivered to all of a user's connections): `notification.message_new`, `notification.mark_read`, `notification.mark_unread`, `notification.added_to_channel`, `notification.removed_from_channel`, `notification.invited`, `notification.invite_accepted`, `notification.invite_rejected`, `notification.mutes_updated`, `notification.channel_mutes_updated`, `notification.channel_deleted`, `notification.channel_truncated`, `user.presence.changed`, `user.updated`, `user.banned`, `user.unbanned`, `health.check`, `connection.recovered`.

### 8.3 Fan-out Design

- Realtime nodes keep an in-memory registry: `connection_id → user, watched channel set`.
- Node subscribes to NATS subjects for each channel it has ≥1 watcher on (interest-based subscription; subjects are `evt.{app}.{cid}` and `usr.{app}.{user_id}`).
- Livestream channels with >5k watchers use a broadcast tier: one subject, per-node local fan-out with write-batching and per-connection outbound queues (slow consumers dropped with `connection.slow` close code).
- Typing events are ephemeral: Redis pub/sub + TTL, never persisted.

---

## 9. REST API Specification

Base URL: `https://host/api/v1`. Auth: `Authorization: <JWT>` + `api_key` query/header. Server-side calls sign with api_secret JWT (`server: true` claim). Full OpenAPI 3.1 document ships in-repo (`/api/openapi.yaml`) and is the source for generated SDK clients.

### 9.1 Endpoint Summary (~90 endpoints)

**Channels**
```
POST   /channels/{type}/{id}/query          # create-or-get + watch + paginate state
POST   /channels/{type}/query               # create-or-get distinct channel by members
POST   /channels                            # query channels (filters/sort/pagination)
POST   /channels/{type}/{id}                # update (full)
PATCH  /channels/{type}/{id}                # partial update (set/unset)
DELETE /channels/{type}/{id}                # delete (?hard_delete=true)
POST   /channels/{type}/{id}/truncate
POST   /channels/{type}/{id}/hide | /show
POST   /channels/{type}/{id}/stop-watching
GET    /channels/{type}/{id}/messages       # by id set
POST   /channels/{type}/{id}/event          # send event (typing, custom)
POST   /channels/read | /channels/{type}/{id}/read   # mark read
POST   /channels/{type}/{id}/unread         # mark unread from message
POST   /channels/delete                     # batch delete (async task)
```

**Members / invites**: member add/remove/roles via channel update payload (`add_members`, `remove_members`, `invites`, `accept_invite`, `reject_invite`, `add_moderators`, `demote_moderators`, `hide_history`), `POST /members` (query members).

**Messages**
```
POST   /channels/{type}/{id}/message        # send
GET    /messages/{id}
POST   /messages/{id}                       # update (full)
PUT    /messages/{id}                       # partial update
DELETE /messages/{id}                       # ?hard=true
GET    /messages/{id}/replies               # thread pagination
POST   /messages/{id}/action                # run message action (command flows)
POST   /messages/{id}/reaction
DELETE /messages/{id}/reaction/{reaction_type}
GET    /messages/{id}/reactions
POST   /messages/{id}/translate
POST   /messages/{id}/commit                # commit pending message
GET    /unread                              # unread counts summary
POST   /search                              # message search
```

**Users**: `POST /users` (upsert batch), `PATCH /users` (partial), `POST /users/query`, deactivate/reactivate/delete (+ batch async), `GET/POST/DELETE /devices`, block/unblock/list-blocked, guest token endpoint.

**Moderation**: ban/unban (`POST/DELETE /moderation/ban`), mute/unmute user & channel, flag/unflag, query banned users, query flags/review queue, blocklist CRUD (`/blocklists`).

**Config (server-side only)**: channel-type CRUD (`/channeltypes`), roles & permissions CRUD (`/roles`, `/permissions`), commands CRUD (`/commands`), webhooks CRUD, push provider config, app settings (`GET/PATCH /app`).

**Files**: `POST /channels/{type}/{id}/image | /file` (multipart or resumable TUS), `DELETE` same paths.

**Tasks/Import/Export**: `GET /tasks/{id}`, `POST /export_channels`, `GET /export_channels/{id}`, `POST /imports` (Stream-format JSON), `POST /users/export`.

**Polls**: poll CRUD, options, votes (`/polls`, `/polls/{id}/vote`...).

### 9.2 Query Filter Syntax

MongoDB-style JSON filters, identical to Stream for drop-in familiarity:

```json
{
  "filter_conditions": {
    "type": "messaging",
    "members": { "$in": ["dhiraj"] },
    "last_message_at": { "$gt": "2026-01-01T00:00:00Z" },
    "custom_project": { "$eq": "FSM-EPC" }
  },
  "sort": [{ "field": "last_message_at", "direction": -1 }],
  "limit": 20, "offset": 0,
  "state": true, "watch": true, "presence": true,
  "message_limit": 25, "member_limit": 30
}
```

Custom-field filters compile to indexed JSONB expressions (GIN indexes on `custom`).

### 9.3 Error Model & Rate Limits

- Stream-compatible error envelope: `{ "code": 4, "message": "...", "status_code": 400, "more_info": "https://..." }` with a stable numeric code registry.
- Rate limit headers on every response: `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`. Defaults: per-app 5k/min server-side, per-user 60 writes/min (all configurable per endpoint).

---

## 10. Authentication & Tokens

- **User tokens**: JWT signed with app secret; claims: `user_id`, optional `exp`, `iat`. Minted by the customer's backend via server SDKs (never in frontend).
- **Development tokens**: unsigned mode toggle for local dev (`disable_auth_checks`).
- **Server tokens**: `{"server": true}` claim → full admin, can act `on behalf of` any user via `user_id` param.
- **Guest & anonymous** flows as in 5.3.
- **Token revocation**: `revoke_tokens_issued_before` per app and per user.
- Optional **RS256/EdDSA** with JWKS endpoint for enterprises that want asymmetric verification at the edge.
- **Webhook signatures**: `X-Signature` HMAC-SHA256 of raw body with api_secret.

---

## 11. Moderation Suite

1. **Blocklists** — named word lists (exact/wildcard/regex modes); behaviors: `block` (reject), `flag` (queue), `shadow_block` (visible only to author). Ships with Stream-equivalent default `profanity_en_2020_v1`-style list.
2. **Automod pipeline** — ordered middleware on message create/update: blocklist → optional **webhook classifier** (your own HTTP endpoint decides; generic, not AI-specific) → decision (`allow`/`flag`/`block`/`shadow`). The pipeline is a pluggable middleware chain, so v2 AI/ML classifier adapters (text + image) slot in without API changes.
3. **Manual tools** — ban (global/channel, timed, IP-optional), shadow ban, mute (timed), freeze channel, delete message, delete+ban combos.
4. **Flag & review queue** — users flag messages/users (with reason + custom data); dashboard review UI with approve/delete/ban actions; `GET /moderation/queue` API.
5. **Pending messages** — `mark_messages_pending` channel types hold messages until moderator `commit`.
6. **Audit log** — every moderation action recorded (actor, target, action, reason, timestamp), queryable + exportable.

---

## 12. Push Notifications

- **Providers**: FCM v1 (HTTP), APNs (p8 token), Web Push (VAPID), Huawei/Xiaomi adapters (community). Multiple named providers per app (e.g., separate iOS apps).
- **Triggers**: `message.new` (offline members, non-muted, non-silent), mentions, reactions to own message (optional), `notification.added_to_channel`, invites.
- **Delivery rules**: skip online users with active WS (configurable "push even if online"), respect user mutes/channel mutes, dedupe multi-device, collapse keys per channel.
- **Templates**: Handlebars templates per event type per provider — `{{ sender.name }}: {{ message.text }}` — editable in dashboard.
- **Payload passthrough** for data-only pushes so mobile SDKs render rich notifications with channel context.
- **Push testing endpoint** (`POST /check_push`) returning rendered payloads per device — parity with Stream's push tester.

---

## 13. Webhooks & Integrations

- **After-events webhook(s)**: subscribe endpoints to any subset of the event catalog; at-least-once delivery, retries with exponential backoff (max 6), signed payloads, dead-letter queue visible in dashboard.
- **Before-message-send hook** (synchronous): external endpoint can mutate or reject a message pre-persist (200ms budget, fail-open/fail-closed configurable).
- **Custom command webhook**: slash commands POST to your handler; respond with message mutation or ephemeral response (giphy-style flows).
- **Queue integrations**: native NATS/Kafka/SQS/RabbitMQ event sinks as an alternative to HTTP webhooks.
- **Bot support pattern**: server-token bots as first-class users; example bots in repo (echo, moderation bot). AI assistant example bot ships with v2.

---

## 14. Search

- Pluggable `SearchBackend` interface; default **Meilisearch** (typo tolerance, fast, easy self-host); **OpenSearch** adapter for large scale; **Postgres FTS** fallback adapter (zero extra infra).
- Indexed: message text, attachment titles, user names, channel names, custom fields (configurable).
- Query API: full-text + the same Mongo-style filters (`channel cid $in`, `created_at` ranges, `mentioned_users`), sort by relevance or recency, cursor pagination.
- Permission-aware: results filtered to channels the requesting user can read (team-aware).
- Async indexing via the event bus; reindex admin command.

---

## 15. File & Media Handling

- Upload via multipart (≤100MB default) or **TUS resumable** protocol for large video (P2).
- Storage adapters: S3, MinIO, GCS, Azure Blob, local disk (dev).
- **Image pipeline**: EXIF strip, virus-scan hook (ClamAV adapter), thumbnail generation (worker, libvips), blurhash generation for instant placeholders.
- **Signed URLs**: time-limited download URLs; optional public CDN mode with cache headers.
- Allow/deny lists for MIME types & extensions per app; image moderation hook (Section 11).
- Attachment size/count limits per channel type.

---

## 16. SDK Matrix

Three layers per platform, mirroring Stream's architecture: **(1) low-level API client → (2) state & offline layer → (3) UI component kit.**

### 16.1 Frontend / Client SDKs

| Platform | Client | State/Offline | UI Kit | Phase |
|---|---|---|---|---|
| **JavaScript/TypeScript** (core) | `@openstream/chat` | built-in reactive state | — | P1 |
| **React** | via JS core | offline via IndexedDB | `@openstream/chat-react` (ChannelList, Channel, MessageList, MessageInput, Thread, reactions, attachments) | P1 |
| **React Native / Expo** | via JS core | SQLite offline | `@openstream/chat-react-native` | P2 |
| **Flutter** | `openstream_chat` (Dart) | `openstream_chat_persistence` (Drift/SQLite) | `openstream_chat_flutter` | P2 |
| **iOS (Swift)** | `OpenStreamChat` | CoreData/SQLite offline | `OpenStreamChatUI` (UIKit) + `OpenStreamChatSwiftUI` | P3 |
| **Android (Kotlin)** | `openstream-chat-android-client` | Room offline | XML UI kit + **Jetpack Compose** kit | P3 |
| **Angular** | via JS core | — | `@openstream/chat-angular` | P3 |
| **Vue** | via JS core | — | `@openstream/chat-vue` (community-first) | P3 |
| **Unity / Unreal** | C# / C++ clients | — | — | P4 (community) |

UI kits ship with theming systems (CSS variables / ThemeData / design tokens), light/dark, RTL, i18n (10+ languages), accessibility (WCAG 2.1 AA), and optimistic UI updates.

### 16.2 Backend / Server SDKs

All generated from the OpenAPI spec + hand-written ergonomic layer:

| Language | Package | Phase |
|---|---|---|
| **Go** | `github.com/openstream/chat-go` (reference; used in server tests) | P1 |
| **Node/TS** | `@openstream/chat` (isomorphic — same package, server mode with secret) | P1 |
| **Python** | `openstream-chat` (sync + asyncio) | P2 |
| **PHP** | `openstream/chat-php` | P3 |
| **Java/Kotlin** | `io.openstream:chat-java` | P3 |
| **.NET (C#)** | `OpenStream.Chat` | P3 |
| **Ruby** | `openstream-chat-ruby` | P3 |

### 16.3 Compatibility Mode (migration killer-feature)

Optional **Stream-compatible API façade** (`--stream-compat` flag): request/response shapes match Stream Chat's v1 API closely enough that official Stream SDKs can point `baseURL` at an OpenStream server for evaluation/migration. Best-effort, documented gaps. This dramatically lowers switching cost.

---

## 17. Scalability & Performance Targets

| Metric | Single node (8 vCPU/16GB) | 5-node cluster |
|---|---|---|
| Concurrent WS connections | 50k (gobwas/epoll) | 250k+ |
| Sustained msg throughput | 2k msg/s | 10k msg/s |
| p99 send→deliver (intra-region) | <250 ms | <250 ms |
| Channel members | 3k regular / unlimited livestream watchers | same |
| Message history | Unbounded (monthly partitions + optional S3 archive tier) | same |

Techniques: interest-based NATS subscriptions, Redis-cached hot channel state, per-connection write coalescing, message table partition pruning, read replicas for query endpoints, pgbouncer, ULID message ids for index locality, backpressure with slow-consumer disconnect, horizontal realtime tier with consistent-hash user→node affinity (optional, improves cache hit rate).

Load-testing harness in repo (`k6` + custom Go WS swarm) with published benchmark methodology.

---

## 18. Deployment & DevOps

- **Quick start**: `docker compose up` → Postgres + Redis + NATS + Meilisearch + MinIO + openstream + dashboard on :3030. Seed CLI creates app + demo users.
- **Kubernetes**: official Helm chart (HPA on realtime pods by connection count metric, PodDisruptionBudgets, NetworkPolicies).
- **Single-binary mode**: embedded SQLite + in-proc pub/sub + local disk for hobby deployments (explicitly "not for production scale").
- **CLI** (`openstream` command): migrations, app/key management, user token mint, import/export, reindex, doctor (env diagnostics).
- Zero-downtime deploys: WS drain with `connection.recovered` client resume; rolling migration policy (expand/contract).
- Config precedence: flags > env > YAML.

---

## 19. Observability

- **Prometheus metrics**: connections, events published/delivered, delivery latency histograms, DB pool stats, webhook success/failure, push delivery, per-endpoint request metrics.
- **OpenTelemetry tracing** end-to-end (HTTP → DB → NATS → WS delivery) with trace-context propagation into webhooks.
- **Structured logging** (slog JSON), request IDs, per-app log scoping.
- Grafana dashboard JSONs + alert rules shipped in `/deploy/observability`.
- Dashboard "live event inspector" (tail events per channel, like Stream's dashboard).

---

## 20. Security & Compliance

- JWT auth everywhere; secrets encrypted at rest (envelope encryption, KMS adapter).
- TLS 1.2+; HSTS; strict CORS per app (configurable origins).
- Input sanitization: server-side HTML sanitization of rendered markdown (bluemonday), custom-field key validation, upload MIME sniffing.
- SQL injection impossible-by-construction (sqlc prepared statements); JSONB filter compiler whitelist.
- Rate limiting + per-IP connection caps + auth brute-force lockout.
- **GDPR**: hard-delete user with cascade, data export per user, retention policies, audit log.
- SSRF protection on URL enrichment & webhooks (deny private IP ranges, DNS-rebinding guard).
- Security disclosure policy + signed releases (cosign) + SBOM.
- E2EE: **out of scope for v1** (like Stream's default) — documented pattern for client-side encryption over custom fields; native E2EE explored post-v1 (Section 24).

---

## 21. Data Import / Export & Migration

- **Import**: async bulk import accepting **Stream Chat's export JSON format directly** (users, channels, members, messages, reactions, devices) — one-command migration off Stream. Validation endpoint returns row-level errors before commit.
- **Export**: async channel export (JSON/CSV) with filters, user data export (GDPR), full-app export for backups; results delivered as signed S3 URLs; `GET /tasks/{id}` polling + webhook completion event.
- **Live migration guide**: dual-write recipe using Stream webhooks → OpenStream import API for zero-downtime cutover.

---

## 22. Roadmap & Release Phases

| Phase | Target | Scope |
|---|---|---|
| **P1 — MVP Core** (months 0–4) | v0.1–v0.3 | Auth/tokens, users, channels + 5 built-in types, messages, threads, reactions, attachments (S3), typing, read state, presence, query channels/users, WS engine + sync, permissions v2 core, basic bans/mutes, JS/TS SDK + React UI kit, Go & Node server SDKs, Docker Compose, dashboard v0 (app/keys/channel-type editor) |
| **P2 — Parity Push** (months 4–8) | v0.4–v0.7 | Search, link previews, slash commands + custom commands, webhooks (before/after), push (FCM/APNs/WebPush), moderation suite (blocklists, flags, queue, shadow bans), invites, slow mode, truncate, guest/anonymous, teams multi-tenancy, import/export, capabilities, React Native + Flutter SDKs, Python SDK, Helm chart, rate limiting |
| **P3 — Full Parity** (months 8–12) | v0.8–v1.0 GA | Polls, scheduled messages, reminders, drafts, translation, pinned/archived channels, campaigns/broadcast, remaining SDKs (iOS, Android, Angular, Vue, PHP/Java/.NET/Ruby), Stream-compat façade, SSE fallback, load-test published benchmarks, docs site complete |
| **P4 / v2 — Beyond** | post-1.0 | **AI Suite (v2)**: AI/LLM streaming messages + `ai_indicator` events, AI message components in UI kits, AI automod classifier adapters (text/image), AI assistant example bot, semantic message search. Plus: activity feeds module, video/voice signaling (LiveKit integration), E2EE research, Unity/Unreal, plugin system (Go plugins/WASM hooks) |

**Governance**: open roadmap (GitHub Projects), RFC process for API changes, semver, LTS releases every 12 months, CLA-free (DCO sign-off), Apache 2.0.

---

## 23. Repository Structure

Monorepo (server + JS SDKs) plus per-platform SDK repos:

```
openstream/openstream            # main monorepo
├── cmd/openstream/              # CLI + service entrypoints
├── internal/
│   ├── api/                     # REST handlers (chi), request/response DTOs
│   ├── realtime/                # WS engine, registry, fan-out
│   ├── domain/                  # entities, permission engine, channel-type config
│   ├── store/                   # sqlc queries, migrations, outbox
│   ├── bus/                     # NATS abstraction (+kafka adapter)
│   ├── search/                  # backend interface + meilisearch/opensearch/pgfts
│   ├── storage/                 # s3/minio/gcs/local adapters
│   ├── push/                    # fcm/apns/webpush
│   ├── moderation/              # pipeline, blocklists, webhook classifier hook
│   ├── webhook/                 # delivery workers, signing
│   └── worker/                  # enrichment, thumbnails, retention, tasks
├── api/openapi.yaml             # source-of-truth API spec
├── dashboard/                   # React admin UI (embedded)
├── sdk/js/                      # @openstream/chat + react kit (workspace)
├── deploy/                      # compose, helm, terraform, observability
├── docs/                        # docusaurus site
├── examples/                    # slack-clone, whatsapp-clone, livestream, support
└── loadtest/                    # k6 + ws swarm
```

---

## 24. Non-Goals (v1) & v2 Scope

**Deferred to v2 (AI Suite):**
- AI/LLM message support: streaming assistant messages, `ai_indicator` thinking/generating states, stop-generation events (M22).
- AI message UI components in all SDK kits (typing indicators for bots, streaming markdown renderer).
- AI automod: ML text/image classifier adapters (Detoxify-style model server, OpenAI/Anthropic moderation API adapters, NSFW image models). v1 ships blocklists + generic webhook classifier only; the middleware pipeline interface is designed so v2 adapters are drop-in.
- AI assistant example bot.
- Semantic/vector message search (Meilisearch hybrid mode reserved).

**Non-goals (v1):**
- Native E2EE (documented client-side pattern only).
- Video/voice calling (integration recipe with LiveKit instead; possible P4 module).
- Activity feeds (separate future module, like Stream Feeds).
- Built-in billing/metering UI (Prometheus usage metrics exposed; billing is the operator's concern).
- Serverless/edge runtime for the server (Go processes only).


---

## 25. Framework Currency & Version Targets

A chat platform's SDKs live inside *other people's* apps, so staying current with each framework's latest stable release is not optional polish — it is a core product requirement. This section defines (a) the current version baselines as of **July 2026**, (b) the modern platform features each SDK must be built on (not retrofitted to), and (c) the standing process that keeps this table from going stale.

### 25.1 Version Baseline Matrix (as of July 2026 — living table, reviewed quarterly)

| Platform / Tech | Current target | Minimum supported | Key modern features we build on |
|---|---|---|---|
| **Go (server)** | **1.26.x** | 1.25 (N-1) | Green Tea GC (default in 1.26, 10–40% lower GC overhead — directly benefits WS fan-out), `iter` range-over-func, PGO builds in release pipeline |
| **PostgreSQL** | 17.x | 16 | `MERGE RETURNING`, logical replication improvements, JSON_TABLE |
| **Node.js (JS tooling & server SDK)** | 24 LTS | 22.11+ (also the React Native floor) | native TS type-stripping, stable `fetch`/WebStreams |
| **TypeScript** | 5.9 → **TS 7 (native/Go-based compiler)** adoption track | 5.6 | TS 7 native compiler is ~10x faster; CI runs `@typescript/native-preview` now so SDKs are ready for 7.0's stricter defaults (`--strict` default, es5 target removed) |
| **React** | **19.2+** | 18.3 | **React Compiler (production-ready)** — UI kit ships compiler-optimized, no manual `useMemo`/`useCallback`; `use()` hook for suspense-based channel loading; Server Components-safe exports (client boundaries marked) for Next.js 15/16 apps |
| **React Native** | **0.84+ (New Architecture only, Hermes V1 default)** | 0.82 (first opt-out-removed release) | New Architecture is now mandatory (legacy arch no longer compiled since 0.84) — our RN SDK is **New-Architecture-only from day one**: Fabric renderer, TurboModules, Nitro Modules for the SQLite offline layer, Reanimated 4 for message-list animations; precompiled iOS builds; track RN 1.0 |
| **Expo** | SDK 54+ (precompiled RN default) | SDK 53 | config-plugin based install, no bare-workflow requirement |
| **Flutter / Dart** | **Flutter 3.44 / Dart 3.12** | Flutter 3.38 | Impeller-only rendering (Android + iOS), **UI-thread merge → direct synchronous FFI to Swift/Kotlin** (used for push token + keychain access without platform channels), Swift Package Manager default on iOS (no CocoaPods), AGP 9 / Kotlin built-in Gradle, Material/Cupertino decoupling tracked, private named parameters (Dart 3.12) |
| **iOS / Swift** | **Swift 6.x (strict concurrency), iOS 26 SDK** | iOS 16 runtime | Swift 6 `Sendable`-checked SDK core, SwiftUI-first UI kit, SwiftPM-only distribution, UIScene lifecycle (Apple moving to require it), Liquid Glass design adoption in default theme |
| **Android / Kotlin** | **Kotlin 2.2.x, AGP 9, Compose (latest BOM), Android 17 (API 37) target** | API 26 min | K2 compiler, Compose-first UI kit (XML kit maintenance-mode), KSP2, 16KB page-size compliance, edge-to-edge default |
| **Angular** | **v22** (Signals & Signal Forms stable) | v20 (zoneless default era) | Signals-based state bindings for channel list/unread counts, zoneless change detection, Vitest (Angular's default test runner since v21) |
| **Vue** | **3.6 (Vapor Mode)** | 3.5 | Composables-first API; UI kit components compatible with Vapor Mode opt-in |
| **Svelte (community kit)** | 5 (Runes) | 5 | `$state`/`$derived` bindings over the JS core client |
| **Redis / Valkey** | Valkey 8 / Redis 7.4 | 7.2 | client-side caching, functions |
| **NATS JetStream** | 2.11+ | 2.10 | per-subject limits, consumer replicas |
| **Meilisearch** | 1.15+ | 1.12 | hybrid/vector-ready search (future semantic message search) |

> **Rule: this table is normative.** Every SDK repo's CI matrix must match the "current target" and "minimum supported" columns. The table is regenerated as part of the quarterly Framework Currency Review (25.3) and any change lands as a versioned PR to this spec.

### 25.2 Design Consequences of "Latest-First"

Building on the latest stable (rather than lowest common denominator) is an explicit strategy:

1. **React kit assumes the React Compiler.** No hand-rolled memoization; smaller, cleaner component code. React 18 consumers get a compiled fallback bundle.
2. **RN SDK never supports the legacy architecture.** Since RN 0.84 removed it entirely, supporting it would double our surface for a shrinking audience. JSI/Nitro-based offline storage from the start.
3. **Flutter SDK uses direct FFI (thread-merge)** instead of platform channels wherever possible — faster and less code. SwiftPM-only iOS plugin; CocoaPods never introduced.
4. **iOS kit is SwiftUI-first with Swift 6 strict concurrency** — the WS client is an `actor`; no `@unchecked Sendable` escapes in public API.
5. **Angular kit is Signals-native** — unread counts and channel ordering are exposed as signals, no RxJS requirement (interop provided).
6. **Server tracks Go's 6-month cadence within one release** — Go's backward-compatibility promise makes this cheap; we gain GC/runtime wins (e.g., Green Tea) for free.

### 25.3 Framework Currency Process (how we stay current)

| Mechanism | Cadence | Detail |
|---|---|---|
| **Automated dependency updates** | Continuous | Renovate bot on every repo (grouped PRs, auto-merge for patch, human review for minor/major); npm/pub/Swift/Gradle/Go modules all covered |
| **Canary CI lanes** | Every release of upstream beta | Each SDK has a nightly CI lane against the *next* framework version (React canary, RN nightly, Flutter beta channel, Angular next, Go rc, Kotlin EAP). Failures open tracking issues automatically — we know about breaking changes months before stable |
| **Quarterly Framework Currency Review** | Q1/Q2/Q3/Q4 | Maintainer rotation reviews each ecosystem's releases + roadmaps (React Conf/Universe, Google I/O, WWDC, KotlinConf, GopherCon, Flutter releases), updates table 25.1, files adoption RFCs for major shifts (e.g., "adopt Vapor Mode", "RN 1.0 migration") |
| **Support-window policy** | Rolling | **Current + previous major (N / N-1)** for frameworks; OS floors reviewed yearly (iOS N-3, Android API 26+ until data says otherwise). Dropping a version requires a deprecation notice one minor release ahead |
| **Security response** | ≤72h for critical | Subscribe to security feeds of every dependency (e.g., the Dec 2025 React2Shell RSC RCE and Metro dev-server RCE showed frontend CVEs can be critical); pinned lockfiles + provenance checks (npm provenance, gosum, SLSA) to resist supply-chain attacks like Shai-Hulud |
| **Docs freshness** | Per release | Docs site version selector per SDK; every code sample CI-executed against the current target so samples can't rot |
| **Public compatibility page** | Auto-generated | openstream.dev/compatibility renders table 25.1 from a machine-readable `versions.yaml` in the monorepo — single source of truth consumed by CI, docs, and the dashboard's "environment check" |

### 25.4 Adoption Triggers (what forces a spec/SDK update)

- Upstream **stable release** of a tracked framework → currency review entry within 2 weeks.
- Upstream **deprecation/removal announcement** (e.g., TS 7 removing `--target es5`, Apple requiring UIScene) → migration issue opened immediately with a deadline.
- **New platform capability that changes chat UX** (e.g., iOS 26 Liquid Glass tabs, Android 17 features, GenUI/A2UI-style agentic UI, WebGPU) → evaluated in the quarterly review for UI-kit adoption.
- **Runtime EOL** (Node LTS end, Go N-2, Postgres EOL) → hard drop scheduled in the next minor.


---

*End of specification — Spec v1.1, July 2026. AI Suite deferred to v2 (Section 24).*
