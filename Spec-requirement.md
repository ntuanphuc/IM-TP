# Requirements Specification for the School Messaging App (Go Backend)

Version 1.2 · November 15, 2026 · For AI-assisted development

## 1. Background and objectives

The school needs an internal messaging app to **replace Microsoft Teams for messaging**. The entire system is **self-hosted** and authenticates **directly against Active Directory over LDAPS**, with no dependency on an external SSO provider or identity provider.

**Scale** — About 11,000 accounts, including around 1,000 teachers and staff and 10,000 students. Expect 2,000–3,000 concurrent connections at peak and design for three times that load.

**Users** include students, teachers, and administrative and operational departments such as HR, finance, school leadership, and IT. All use the same **segment and rule** model (section 7). Manage department members and managers manually in the app because AD does not synchronize them automatically.

**Primary objectives**

- Support direct messages, group chats, and channels for classes, subjects, and departments
- Sign in with existing AD accounts and automatically synchronize users, groups, and channel membership
- Occasionally broadcast to all 10,000 students or to specific grades or classes
- Protect students who are minors with policies that control who can message whom
- Keep the system manageable for a small IT team with few components and straightforward backup and monitoring

## 2. Scope

The **MVP** includes chat, channels, threads, reactions, mentions, files, search, AD authentication, segments and policies for students and departments, manual membership management, broadcasts, push notifications, audit logs, retention, and an admin API.

Chat is available on iOS, Android, and browsers. Administrators use a separate web app.

Later phases cover voice and video calls, screen sharing, bots, webhooks, slash commands, end-to-end encryption, a workflow builder, and third-party integrations.

The school has at least two domain controllers that support LDAPS, an Apple Developer account, and its own Firebase project. Infrastructure runs on-premises with Docker.

## 3. Required technology stack

Use the technologies listed below for AI-assisted development.

| Category | Technology |
| --- | --- |
| Language | Go (latest stable version) |
| HTTP router | `chi` |
| WebSocket | `github.com/coder/websocket` |
| Database access | `pgx` + `sqlc` (generate code from SQL) |
| Migration | `goose` |
| Database | PostgreSQL |
| Cache, presence, pub/sub | Redis (`go-redis`) |
| Queue | NATS JetStream (`nats.go`) |
| Search | OpenSearch with ICU analysis and folding for Vietnamese diacritics |
| Object storage | MinIO (`minio-go`) with ClamAV virus scanning |
| LDAP | `github.com/go-ldap/ldap/v3` |
| JWT | `github.com/golang-jwt/jwt/v5` |
| Push | FCM (Firebase Admin SDK cho Go), APNs (`sideshow/apns2`) |
| Logging | `log/slog` (JSON) |
| Metrics/tracing | Prometheus + OpenTelemetry |
| Configuration | Environment variables and YAML files, validated at startup |
| Test | `testing`, `testify`, `testcontainers-go` |
| Packaging | Docker, Docker Compose for development, and k3s or Compose/Swarm for production |

## 4. Architecture and repository layout

Use a **modular monolith** with a `server` binary for the API and WebSocket and a `worker` binary for push notifications, broadcasts, file scanning, AD synchronization, and indexing. Both share one codebase organized by package.

```
/cmd/server        # HTTP + WebSocket entrypoint
/cmd/worker        # queue consumers, scheduled jobs
/internal/auth     # LDAP, JWT, refresh token, break-glass admin
/internal/directory# AD synchronization (users, groups, role mapping)
/internal/chat     # channel, message, thread, reaction, read state
/internal/realtime # WebSocket hub, presence, typing, resume
/internal/policy   # messaging rules and role-based permissions
/internal/broadcast# audience resolution, scheduling, stats
/internal/push     # device registry, FCM/APNs senders
/internal/files    # upload, MinIO, scan status
/internal/search   # indexer, query
/internal/audit    # append-only audit log
/internal/retention# policy-driven deletion and archiving jobs
/internal/platform # config, db, redis, nats, logger, metrics
/migrations        # goose SQL
/api               # openapi.yaml (REST API source of truth)
/deploy            # docker-compose, k8s manifests
```

**Architecture rules**

- Each `internal/*` package exposes only the necessary interfaces and avoids circular imports
- Write all SQL queries in `.sql` files and generate code with `sqlc`
- Keep the server **stateless** by storing state in PostgreSQL or Redis so multiple instances can run behind a load balancer
- Synchronize WebSocket traffic across instances with Redis pub/sub
- Propagate `context.Context` throughout and set timeouts on all external calls

### Monorepo and deployment boundaries

```text
backend/                     # Go module
  cmd/server/                # REST, authentication, WebSocket
  cmd/worker/                # AD sync, outbox, search, file, push, retention
  internal/                  # business modules listed above
  migrations/
apps/
  mobile/                    # React Native + TypeScript for iOS and Android
  web-chat/                  # React + TypeScript chat app
  admin-web/                 # React + TypeScript static build
packages/
  api-client/                # REST client and types generated from OpenAPI
  realtime-contract/         # WebSocket payload types and validation
api/
  openapi.yaml
  realtime/                  # JSON Schema and WebSocket examples
deploy/
docs/
```

- The `cmd`, `internal`, and `migrations` directories in the backend layout are under `backend/`. The three TypeScript apps share a workspace and lockfile.
- **Mobile** is organized into authentication, chat, files, search, and notifications. The data layer manages REST, WebSocket, resumption, and the retry queue keyed by `client_msg_id`. Store refresh tokens in the operating system's secure storage. Clear local chat data on sign-out or confirmed session revocation.
- **Admin web** is organized into directory, segments, approvals, broadcasts, audit, and retention. The reverse proxy serves static assets and forwards `/v1` on the same origin. Keep access tokens in memory. Use an `HttpOnly`, `Secure`, `SameSite` cookie and CSRF protection for refreshes.
- **Web chat** is organized into authentication, channels, messages, threads, files, search, and announcements. Serve the static build on a separate origin from the admin app. Its reverse proxy serves the frontend and forwards `/v1` REST and WebSocket traffic to the same backend.
- Clients share contracts, types, and the API client. The backend enforces policies and records audit events. Each app owns its UI and state. CI checks that generated code matches the contracts and builds and tests each app. The server, worker, web apps, and mobile app have separate release processes. Maintain `/v1` compatibility with supported mobile versions.

## 5. Data model

Use **ULIDs** (26-character text) for `channels`, `messages`, and `files`. Use UUIDs for the other tables. Every table has `created_at`. Mutable tables also have `updated_at`.

- **users**: id, ad\_guid (unique), username, display\_name, email, status (`active|disabled`), org\_unit, grade (smallint, nullable), class\_code (nullable), manager\_id (nullable, entered manually and optional), is\_system\_admin (bool), avatar\_key, last\_synced\_at. Derive permissions from segments (section 7) and omit a fixed role column.
- **ad\_groups** / **user\_ad\_groups**: dn, name, with a many-to-many relationship to users
- **segments**: id, key (unique, for example `student:grade10` or `dept:hr`), name, mode (`ad|manual|hybrid`), ad\_group\_dn (nullable, used for `ad` and `hybrid`), is\_minor (bool), sensitive (bool), review\_interval\_days (nullable)
- **segment\_members**: segment\_id, user\_id, role (`member|manager|owner`), source (`ad_sync|manual`), suppressed (bool), added\_by, added\_at, expires\_at (null), last\_reviewed\_at
- **segment\_change\_requests**: id, segment\_id, action (`add|remove|set_role`), target\_user\_id, requested\_by, approved\_by (nullable), status (`pending|approved|rejected`). Used for `sensitive` segments.
- **segment\_reviews**: id, segment\_id, due\_at, completed\_by (null), completed\_at (null), removed\_count
- **policy\_rules**: id, subject\_segment, action, object\_segment (null), scope (jsonb), effect (`allow|deny`), conditions (jsonb), priority (int), enabled
- **channels**: id, type (`public|private|dm|group_dm|announcement`), name, topic, class\_code (nullable), segment\_id (nullable, links a channel to a segment), created\_by, archived\_at, **last\_seq bigint**
- **channel\_members**: channel\_id, user\_id, role (`owner|member`), **last\_read\_seq**, muted\_until, notif\_pref (`all|mentions|none`), source (`manual|segment`), joined\_at
- **messages**: id, channel\_id, **seq bigint**, sender\_id, parent\_id (nullable, used for threads), body, attachments (jsonb), edited\_at, deleted\_at. Use a regular table in the MVP with `PRIMARY KEY (id)` and `UNIQUE (channel_id, seq)`. Evaluate partitioning once operational data is available.
- **reactions**: message\_id, user\_id, emoji
- **files**: id, uploader\_id, channel\_id, object\_key, name, mime, size, sha256, scan\_status (`pending|clean|infected`)
- **devices**: id, user\_id, platform (`ios|android|web`), push\_token, app\_version, last\_seen\_at
- **refresh\_tokens**: id, user\_id, device\_id, token\_hash, expires\_at, revoked\_at, replaced\_by
- **broadcasts**: id, created\_by, audience (jsonb, segment expression), message\_id, scheduled\_at, status (`draft|scheduled|sending|done|failed|cancelled`), stats (jsonb)
- **push\_jobs**: id, broadcast\_id (null), device\_id, status, attempts, last\_error
- **policies**: key, value (jsonb), updated\_by (general settings such as quiet hours and blocked keywords)
- **retention\_rules**: scope (`global|channel_type|channel`), scope\_ref, keep\_days
- **audit\_logs**: id, actor\_id, action, target\_type, target\_id, metadata (jsonb), ip, created\_at. Allow `INSERT` only by revoking `UPDATE` and `DELETE` at the database role level.

**Sequence allocation.** When sending a message, execute `UPDATE channels SET last_seq = last_seq + 1 WHERE id = $1 RETURNING last_seq` and insert the message with the returned sequence number in **one transaction**. Sequence numbers are consecutive and unique within each channel.

**Read state.** Advance `last_read_seq` to the highest acknowledged sequence, capped by `last_seq`. Count only visible messages the user may read whose `seq > last_read_seq`. The initial read position depends on whether the user can see prior history when joining.

## 6. AD authentication and sessions

**Sign-in** uses `POST /v1/auth/login` with `username`, `password`, and `device` (platform and optional push token).

1. Try the domain controllers in order over LDAPS with short timeouts and a circuit breaker.
2. Bind with a read-only service account and look up the user by `sAMAccountName` or `userPrincipalName`.
3. Bind again with the user's DN and supplied password to authenticate.
4. Read `memberOf`, `userAccountControl`, and `objectGUID`. Map AD groups to **segments** in YAML, for example `CN=HocSinh` to `student`. Apply this mapping only to `ad` and `hybrid` segments. Manual segments are independent of AD.
5. Upsert the user, synchronize `source=ad_sync` membership, and issue a **15-minute access JWT** and a **30-day refresh token**. Rotate the refresh token and store its hash.

**Requirements**

- Never log or store passwords
- Refresh tokens without calling AD so active sessions survive an AD outage
- Synchronize every 15 minutes. Set users disabled or deleted in AD to `status=disabled`, revoke all their refresh tokens, and disconnect their WebSockets
- Enforce sign-in rate limits by IP address and username using Redis
- Provide one or two local **break-glass admin** accounts with Argon2id password hashes for AD outages. Audit every use
- Revoke the device's entire token chain when an old refresh token is reused

**Session revocation.** REST checks user and session state through a shared store. WebSocket checks at connection, at refresh, and before processing or emitting protected data. Disabling an account invalidates sessions after commit and closes sockets within 60 seconds. Track manual locks separately from AD state. Infer deletion only after successful reconciliation. The MVP maps direct group memberships. Use OpenLDAP in CI and test LDAPS, `objectGUID`, account disabling, and failover against staging AD.

## 7. Authorization, segments, and policies

Compute permissions from **segments** and **rules**. Deny by default. Any matching deny overrides all matching allows. `priority` orders rules of the same effect for decision explanations. Grade-level DM exceptions require an exclusion in the deny rule and an explicit allow rule. Recheck permissions on reads, sends, membership changes, and realtime delivery. Suspend sends in a group DM that loses its last teacher until it meets the requirement again.

**Segments.** Each segment has a `key`, such as `student`, `student:grade10`, `teacher`, `staff`, `dept:hr`, `dept:finance`, or `management`. The `mode` is `ad` for AD group sync, `manual` for in-app management, or `hybrid` for both. The `is_minor` flag applies default safeguards to student segments. The `sensitive` flag enables two-person approval and stricter review for groups such as HR, finance, and school leadership. A user may belong to several segments, such as `teacher` and `dept:math`. Combine permissions across all segments, calculate them at sign-in, cache them in Redis, and invalidate the cache on membership changes.

**Default rule examples** configurable in `policy_rules`

| Subject | Action | Object | Effect |
| --- | --- | --- | --- |
| `student` | `dm.create` | `student` | deny (default, configurable by grade) |
| `student` | `dm.create` | `teacher` | allow |
| `student` | `channel.create` | Any type | deny |
| Any segment | `group_dm.create` | Group with a `student` member | allow when at least one member is a `teacher` |
| `teacher` | `channel.create` | Any type | allow |
| `teacher` | `broadcast.send` | Assigned class | allow |
| `staff` | `dm.create` | `staff` | allow |
| `dept:hr` | `channel.create` | private | allow |
| `management` | `broadcast.send` | Any audience | allow |
| `dept:X` manager | `broadcast.send` | `dept:X` | allow |
| `dept:X` manager | `segment.member.manage` | `dept:X` | allow |
| Any segment | `message.delete_others` | Channel the user owns | allow |
| `student`, `guest` | `broadcast.send`, `audit.read` | Any audience | deny |

**Manual membership and general requirements.** AD supplies identities. Manage department members and managers directly in the app.

- In `manual` or `hybrid` segments, authorized owners, managers, or system administrators add and remove `source=manual` memberships. AD sync writes only `source=ad_sync` rows and preserves manual rows.
- Set `suppressed=true` to exclude a user who still belongs to the AD group so the next sync does not re-add them.
- A `manager` can add or remove members and approve join requests for their segment. An `owner` can also add managers. Only system administrators can create segments and assign the first owner. Other participants have the `member` role.
- Use `expires_at` for temporary membership. A daily job removes expired memberships.
- **Periodic review** uses `review_interval_days` and defaults to six months. Remind managers to confirm the roster. After the deadline, remove unconfirmed members or notify an administrator according to configuration.
- All membership changes in `sensitive` segments go through `segment_change_requests` and require a second approver.
- Offboarding removes users disabled or deleted in AD from all segments, including manual memberships, and revokes their sessions.
- For a segment-linked channel (`channels.segment_id`), membership changes automatically update `channel_members` with `source=segment`. Synchronization preserves entries added manually with `source=manual`.
- Segments marked `is_minor` automatically apply default safeguards. Block student-to-student DMs, require a teacher in group DMs with students, suppress push notifications during quiet hours while preserving messages, support configurable keyword filtering, and allow message reports to teachers or administrators.
- Data isolation is **optional per segment and off by default**. When enabled for a `sensitive` segment, only members can see channels and messages, including in search. System administrators need a separate grant to read content, and every read is audited.
- Keep all authorization checks in `internal/policy` as pure, testable functions called by REST and WebSocket. Audit every membership and rule change.

**Two-person approval.** Sensitive segments use `manual` mode in the MVP. Manual changes, including CSV imports and role changes, require an authorized approver other than the requester. Expiry and offboarding block access immediately. Record audit entries in the same transaction as membership changes, approvals, or rejections.

## 8. REST API

Use the `/v1` prefix. Define the contract in `api/openapi.yaml` before implementing handlers. Authenticate with `Authorization: Bearer <JWT>`. Return errors as `{ "error": { "code", "message" } }`. Use cursor pagination for lists.

**Auth:** `POST /auth/login`, `POST /auth/refresh`, `POST /auth/logout`, `GET /me`, `PUT /me/devices` (register or update the push token)

OpenAPI defines body-based refresh tokens for mobile and host-scoped refresh cookies for web chat and web admin. Sign-in and refresh set the web cookie. Sign-out revokes the session and clears the cookie. Reject refresh requests that supply both a body token and a cookie.

**Users and directory:** `GET /users` (search by name, class, or grade), `GET /users/{id}`, `GET /users/{id}/presence`

**Channels:**

- `GET /channels` (the caller's channels with unread counts), `POST /channels`, `GET /channels/{id}`, `PATCH /channels/{id}`, `POST /channels/{id}/archive`
- `GET/POST/DELETE /channels/{id}/members`
- `POST /dms` (open or retrieve a DM, subject to policy)
- `POST /channels/{id}/read` with `{ "seq": N }` (update `last_read_seq`)
- `PUT /channels/{id}/prefs` (mute, notif\_pref)

**Messages:**

- `GET /channels/{id}/messages?before_seq=&after_seq=&limit=` (50 by default, maximum 100)
- `POST /channels/{id}/messages` with `body`, `parent_id`, `attachment_ids`, and `client_msg_id` as the **idempotency key**. A retry with the same `client_msg_id` preserves a single message.
- `PATCH /messages/{id}`, `DELETE /messages/{id}` (soft delete)
- `PUT/DELETE /messages/{id}/reactions/{emoji}`
- `GET /messages/{id}/thread`
- `POST /messages/{id}/report`

**Files:** `POST /files` (multipart or presigned URL), `GET /files/{id}` (serve only when `scan_status=clean` and the user has channel access). File sizes and types are configurable.

**Search:** `GET /search?q=&type=messages|files|users|channels&channel_id=&from=&to=`. Always **filter results by the caller's permissions**.

**Broadcast:** `POST /broadcasts`, `GET /broadcasts`, `GET /broadcasts/{id}` (with statistics), `POST /broadcasts/{id}/cancel`

**Segments:** `GET/POST /segments`, `PATCH /segments/{id}`, `GET /segments/{id}/members`, `POST /segments/{id}/members`, `PATCH/DELETE /segments/{id}/members/{user_id}` (change role, set expiration, or remove), `POST /segments/{id}/members/import` (CSV), `POST /segments/{id}/review` (confirm review), `GET /segment-change-requests`, `POST /segment-change-requests/{id}/approve` and `/reject`. Every endpoint checks the caller's segment role.

**Admin:** `GET/PUT /admin/policies`, `GET/POST/PUT/DELETE /admin/policy-rules`, `GET/PUT /admin/retention`, `GET /admin/audit`, `POST /admin/directory/sync`, `GET /admin/reports`, `POST /admin/users/{id}/disable`

**Message idempotency.** Store the unique `(channel_id, sender_id, client_msg_id)` key, payload fingerprint, and message ID in the sequence allocation transaction. Concurrent retries with the same payload return the same ID and sequence. A different payload returns `409`. Retain keys for at least seven days, including after message deletion.

## 9. WebSocket protocol

Use `GET /v1/ws`. Mobile clients send `Authorization: Bearer <JWT>` when connecting. Web chat opens same-origin WSS and sends its access token in the first `auth` frame. During browser upgrades, check `Origin` against the allowlist, limit unauthenticated connections, and close them after five seconds if authentication is incomplete. Allow subscriptions and data sends only after `auth.ok`. Redact tokens from logs whether they arrive in a header or an `auth` frame. JSON frames use `{ "type", "id", "data" }`.

**Client → server:** `ping`, `typing` (`channel_id`), `presence.set`, `auth`, `resume` (send `{ channel_id: last_event_cursor }` for each open channel)

**Server → client:** `auth.ok`, `auth.expiring`, `pong`, `message.new`, `message.updated`, `message.deleted`, `reaction.changed`, `typing`, `presence.changed`, `channel.updated`, `member.changed`, `read.updated`, `broadcast.new`

**Requirements**

- Send a heartbeat every 25 seconds. The server closes connections after 60 seconds without a signal.
- **Resume:** Persist the event cursor and payload in the transaction that creates, edits, or deletes a message or changes a reaction. The worker publishes via a transactional outbox and retries with the same event ID. On resume, buffer live events, replay through a fixed cutoff, then merge and deduplicate by cursor. Affected sockets must catch up after Redis reconnects. An expired cursor requires a fresh snapshot. Presence and typing are live-only events.
- When a token is expiring on an open connection, send `auth.expiring`. The client refreshes and sends the new token in an `auth` frame.
- Rate-limit sends per user, for example 20 messages per 10 seconds. Return an error without disconnecting the client.
- Each instance keeps an in-memory hub and uses Redis pub/sub to reach users connected to other instances.
- Store presence in Redis with a TTL. Typing events are ephemeral.

## 10. Broadcasts to 10,000 students

Store **one message record** in an announcement channel. Fan out only push notifications.

**Broadcast flow**

1. An authorized user calls `POST /broadcasts` with an `audience` **segment expression**, such as `student:grade10`, `dept:hr`, or `student:grade10 AND NOT student:class10A3`, and optionally `scheduled_at`.
2. The server checks `broadcast.send` and the sender's audience scope. For example, the `dept:finance` manager can send to `dept:finance` only, and a teacher can send only to assigned classes if the rule requires it.
3. At the scheduled time, the worker rechecks permission, resolves the audience, and commits the recipient snapshot, announcement channel message, and outbox event in one transaction. A uniqueness constraint ensures one message per broadcast. Emit `broadcast.new` after commit.
4. Resolve devices from the recipient snapshot, write `push_jobs`, enqueue them in NATS, and send in batches. FCM accepts up to 500 tokens per batch. Use bounded concurrency for APNs, a **global rate limit**, and exponential backoff on retries.
5. Remove invalid or unregistered tokens from `devices`.
6. Track total recipients, successful and failed push deliveries, and readers who acknowledge via idempotent `PUT /broadcasts/{id}/read`.

**Broadcast access.** Check audience scope at creation and sender permissions at execution. Snapshot recipients when sending begins. Store one message and the broadcast-to-user relationship. The feed, WebSocket, search, files, and push check that relationship, account status, and current data isolation rules. New members receive only broadcasts whose snapshot includes them. Push jobs use a unique broadcast/device key and check cancellation before each batch. A retry can deliver a duplicate if the worker fails after the provider accepts the push and before the result is saved.

**Performance requirements.** Complete pushes to 10,000 devices in under two minutes while limiting the increase in regular message latency to 20%. Support cancellation during sending by dropping unsent jobs.

## 11. Push notifications

- Register devices through `PUT /me/devices`. One user can register multiple devices.
- Suppress push if the user is online elsewhere and has already read the message, has muted the conversation or enabled DND, is outside the permitted notification window, or has a mismatched `notif_pref`. The `mentions` preference sends only for an @mention.
- Keep **push content minimal**, such as the sender's name and "New message". Fetch details from the server when the app opens. Make content visibility configurable.
- Collapse notifications from the same channel to reduce noise.
- Load APNs credentials and the FCM service account from secrets kept outside the repository.

## 12. Files

- Upload to MinIO. The server stores metadata and queues a ClamAV scan. Downloads are unavailable while a scan is `pending`.
- Delete `infected` files, record an audit event, and notify the sender.
- Detect MIME type from file contents, default to a 50 MB size limit per file, and make allowed file types configurable.
- Issue short-lived presigned download URLs after checking permission.
- Generate image thumbnails.

## 13. Search

- Index messages, file names, users, and channels in OpenSearch in near real time, with less than five seconds of delay.
- Use ICU folding for **Vietnamese with and without diacritics** and support prefix searches.
- Include channel and permission data with each document. Always **filter results by the searcher's memberships**.
- Remove deleted or expired messages from the index.
- Provide a command to reindex everything from PostgreSQL.

## 14. Retention, audit, and data

- **Retention:** Run a daily job to delete or archive messages according to `retention_rules`. Default to channel-type rules and provide a way to archive class channels at the end of the school year.
- **Audit log:** Record at least successful and failed sign-ins, break-glass access, policy rule changes, **all segment membership and role changes including AD sync**, approvals and rejections, periodic reviews, deletion of another user's messages, manual channel membership changes, broadcasts, data exports, and account disabling. Keep audit entries append-only through the app.
- **Backups:** Back up PostgreSQL (base backups and WAL) and MinIO. Provide restore scripts and test recovery periodically.
- Keep students' personal data within school infrastructure. Document external data flows, including push metadata sent to APNs and FCM.

## 15. Nonfunctional requirements

| Category | Target |
| --- | --- |
| Message send latency (p95) | Under 300 ms on the internal network |
| Concurrent connections | 3,000 sustained, load-tested to 10,000 |
| Availability | 99.9%, with a PostgreSQL replica and failover and at least two server instances |
| Security | TLS throughout, OWASP ASVS Level 2, input validation, parameterized queries, strict CORS, secure headers, and dependency scanning in CI |
| Encryption at rest | Encrypt disks or volumes and keep secrets outside the repository |
| Observability | Metrics for WebSocket connections, message latency, queue sizes, and push failures, `request_id` in logs, OpenTelemetry traces, Grafana dashboards, and basic alerts |
| Language support | Error messages and system content support Vietnamese and English |
| Time zones | Store UTC and display in the client's time zone, defaulting to `Asia/Ho_Chi_Minh` |

## 16. Testing and quality criteria

- **Unit tests** for `policy`, sequence allocation, audience resolution, and group-to-role mapping with at least 80% coverage for those packages.
- **Integration tests** with `testcontainers-go` for PostgreSQL, Redis, NATS, MinIO, and an LDAP test server such as OpenLDAP. Test at least one successful and one unauthorized request for each endpoint.
- **Realtime tests:** Disconnect and resume without losing or duplicating messages. Retry with the same `client_msg_id`.
- **Load tests** using k6 or `vegeta`: sustain 3,000 concurrent WebSockets under realistic message load and broadcast to 10,000 devices through mocked FCM and APNs.
- **Security tests:** Cover cross-channel IDOR, search permission leaks, revoked tokens, and sign-in brute force.
- CI requires `go vet`, `golangci-lint`, tests, `govulncheck`, and Docker image builds.

Run load tests at both 3,000 and 10,000 WebSocket connections and record send load, channel sizes, and environment settings. Measure p95 from API request receipt through the commit response. Compare p95 with and without a broadcast under the same load. Measure the two-minute target from worker start until the mock provider has accepted every job or jobs have failed permanently. Initial recovery targets are a five-minute RPO and a 60-minute RTO. Verify them with PostgreSQL and MinIO recovery drills. Test concurrent retries, mid-session revocation, recipient-level broadcast isolation, and Redis outages while WebSockets remain open.

## 17. Milestones and acceptance criteria

Each milestone must **run, pass tests, and have documentation** before work proceeds to the next.

**M0 – Project setup.** Create the repository layout, development Docker Compose stack (PostgreSQL, Redis, NATS, MinIO, OpenSearch, OpenLDAP), first migration, CI, and complete MVP `openapi.yaml`. Accept when `make test` passes after `docker compose up`.

**M1 – Authentication and directory.** Implement LDAPS sign-in, JWTs and refresh rotation, break-glass admin, AD synchronization, and group-to-segment mapping. Accept when a test OpenLDAP user can sign in and disabling that user revokes their session after the next sync.

**M2 – Core chat.** Implement channels, DMs, members, messages, sequences, threads, reactions, read state, allow and deny rules, and manual segment membership. Verify that policy can block student-to-student DMs, department managers can change membership only in their segment, AD sync preserves manually added members, and reusing a `client_msg_id` does not create a second message.

**M3 – Realtime.** Implement the WebSocket hub, presence, typing, resume, and Redis pub/sub across two instances. Verify that a client disconnected for 30 seconds receives every message in order after reconnecting.

**M4 – Files and search.** Implement uploads, ClamAV scanning, thumbnails, OpenSearch, and permission filtering. Verify that infected files are blocked and nonmembers cannot find a channel's messages.

**M5 – Push and broadcasts.** Implement device registration, FCM and APNs delivery, announcement channels, segment-expression targeting by grade, class, or department, scheduling, and statistics. Verify that a broadcast to 10,000 mock devices finishes in under two minutes.

**M6 – Operations and hardening.** Complete audit, retention, the admin API, periodic segment membership reviews, metrics, backup and restore, load testing, security review, and operational documentation. Meet the targets in sections 15 and 16.

**App integration milestones.** M0 scaffolds all three apps and the API client generation workflow. M1 verifies sign-in in all three. M2–M3 integrate chat and realtime on mobile and web chat and segment management in web admin. M4–M5 add files, search, and broadcast reception to both chat apps, mobile push, and broadcast creation to web admin. M6 completes operational screens and web chat tests from section 18. Audit each action from the milestone that introduces it. The native desktop app belongs to a later phase.

## 18. Web chat specification

**Audience and scope.** Students, teachers, and staff chat in the browser through `apps/web-chat`. At acceptance, the MVP supports current stable desktop Chrome, Edge, Firefox, and Safari. The responsive UI works on narrow screens. Web Push, PWA support, and offline use across page reloads belong to a later phase. MVP web notifications appear in the app while the tab is open.

### User flows

- Sign in with AD, restore the session after reloading, and sign out. Show unread counts, mute state, and connection status in the channel and DM list. Find people in the directory and open direct or group messages through the API subject to policy.
- Load the timeline by cursor. Allow users to load history and jump to messages linked from search or announcements. Support composing, @mentions, permission-based edits and deletion, thread replies, reactions, and reports. Use the same unread rules as mobile. Acknowledge reads only when the conversation is visible and the timeline has reached the relevant sequence.
- Upload files and show scan and rejection states. Offer API downloads only for clean files. Show search results and announcements for the user's audience and let users acknowledge each broadcast when viewing it.
- Support Vietnamese and English, keyboard navigation, visible focus, and screen-reader labels. Provide states for loading, empty results, disconnection, failed sends, expired sessions, and lost access.

### Browser sessions, realtime, and data

- Keep access tokens and chat data in memory. Use a host-scoped refresh cookie with `HttpOnly`, `Secure`, and `SameSite`. Check Origin and CSRF on endpoints that use cookies. Synchronize sign-out across tabs and serialize refreshes across same-origin tabs to avoid token-chain revocation from concurrent refreshes.
- Use authenticated WebSocket, resume as described in section 9, and reconnect with backoff. Keep the cursor in memory, deduplicate after replay, and reload the snapshot when the cursor expires. Keep pending messages in the tab and retry with the original `client_msg_id`. After reloading, fetch the committed timeline state from the server.
- Escape user input and sanitize rich text when rendering it. On sign-out, session revocation, or lost channel access, disconnect or unsubscribe as appropriate, clear affected in-memory data, and stop retries. Return private responses with `Cache-Control: no-store`.
- Base available controls on backend permissions and handle `401/403` from REST or WebSocket. Apply the same server-side checks to routes and direct links for channels, messages, search, and files.

### Acceptance criteria

- Sign in with two browsers, send and edit messages, open a thread, and synchronize unread state. Disconnect one tab for 30 seconds, send messages from the other, then reconnect. Verify ordered catch-up, edits, and no duplicates after retries.
- When access tokens expire in two tabs at once, refresh preserves a valid session. Signing out in one tab ends the session in all same-origin tabs. WebSockets from outside the origin allowlist and unauthenticated sockets receive no protected data.
- Revoke channel access while the timeline is open. Verify that the UI clears affected content and that REST, WebSocket, search, and direct links deny access. Only eligible recipients can see a broadcast. Pending files cannot be downloaded.
- Test supported browsers and keyboard operation for sign-in, the channel list, composing, and sending at desktop and narrow widths.

## 19. Using this specification for AI-assisted development

1. Provide **the entire specification** as persistent context, or save it as `CLAUDE.md` or `SPEC.md` in the repository. Instruct the AI to follow the stack and layout in sections 3 and 4.
2. Work on **one milestone at a time**, in order from M0 to M6, with one small group of features per step, such as a package or a group of endpoints.
3. Write tests before or alongside each change and get them passing before moving on.
4. Start with `openapi.yaml` and migrations. Review both carefully because the rest of the implementation depends on them.
5. Require **human review** of authentication and LDAP, authorization policies, permission-filtered search, token handling, sequence allocation, and all student data handling.
6. After each milestone, run linting, tests, and `govulncheck`, and conduct a separate security review. AI can help check OWASP ASVS, and a human reviewer remains responsible for the review.
7. Record design decisions in `docs/decisions.md` to keep future AI-assisted work consistent.
