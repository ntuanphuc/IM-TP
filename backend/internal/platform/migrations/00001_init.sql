-- +goose Up
CREATE TABLE users (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), ad_guid text UNIQUE,
 username text UNIQUE NOT NULL, display_name text NOT NULL,
 admin boolean NOT NULL DEFAULT false, disabled boolean NOT NULL DEFAULT false,
 ad_disabled boolean NOT NULL DEFAULT false, student boolean NOT NULL DEFAULT true,
 teacher boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE sessions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL REFERENCES users(id),
 expires_at timestamptz NOT NULL, revoked_at timestamptz
);
CREATE TABLE refresh_credentials (
 digest text PRIMARY KEY, session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 used_at timestamptz
);
CREATE TABLE channels (
 id text PRIMARY KEY, name text NOT NULL, kind text NOT NULL CHECK(kind IN ('private','dm','group_dm','announcement')),
 dm_key text UNIQUE, owner_id uuid NOT NULL REFERENCES users(id), last_seq bigint NOT NULL DEFAULT 0,
 archived_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE channel_members (
 channel_id text NOT NULL REFERENCES channels(id), user_id uuid NOT NULL REFERENCES users(id),
 last_read_seq bigint NOT NULL DEFAULT 0, PRIMARY KEY(channel_id,user_id)
);
CREATE TABLE messages (
 id text PRIMARY KEY, channel_id text NOT NULL REFERENCES channels(id),
 seq bigint NOT NULL, sender_id uuid NOT NULL REFERENCES users(id),
 parent_id text REFERENCES messages(id) ON DELETE SET NULL, body text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), edited_at timestamptz, deleted_at timestamptz,
 UNIQUE(channel_id,seq)
);
CREATE TABLE message_requests (
 channel_id text NOT NULL REFERENCES channels(id), sender_id uuid NOT NULL REFERENCES users(id),
 client_msg_id text NOT NULL, fingerprint text NOT NULL, message_id text NOT NULL, seq bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(channel_id,sender_id,client_msg_id)
);
CREATE TABLE reactions (
 message_id text NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 user_id uuid NOT NULL REFERENCES users(id), emoji text NOT NULL, PRIMARY KEY(message_id,user_id,emoji)
);
CREATE TABLE events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, channel_id text NOT NULL REFERENCES channels(id),
 kind text NOT NULL, payload jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), published_at timestamptz
);
CREATE INDEX events_pending ON events(id) WHERE published_at IS NULL;
CREATE TABLE audit_logs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, actor_id uuid REFERENCES users(id),
 action text NOT NULL, target text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE segments (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), key text UNIQUE NOT NULL, name text NOT NULL,
 sensitive boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE segment_members (
 segment_id uuid NOT NULL REFERENCES segments(id), user_id uuid NOT NULL REFERENCES users(id),
 role text NOT NULL CHECK(role IN ('member','manager','owner')), PRIMARY KEY(segment_id,user_id)
);
CREATE TABLE segment_requests (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), segment_id uuid NOT NULL REFERENCES segments(id),
 user_id uuid NOT NULL REFERENCES users(id), requester_id uuid NOT NULL REFERENCES users(id),
 approver_id uuid REFERENCES users(id), role text NOT NULL, status text NOT NULL DEFAULT 'pending',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE reports (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, message_id text NOT NULL REFERENCES messages(id),
 reporter_id uuid NOT NULL REFERENCES users(id), reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE broadcasts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), actor_id uuid NOT NULL REFERENCES users(id),
 segment_id uuid NOT NULL REFERENCES segments(id), body text NOT NULL, scheduled_at timestamptz NOT NULL,
 status text NOT NULL DEFAULT 'scheduled', message_id text REFERENCES messages(id)
);
CREATE TABLE broadcast_recipients (
 broadcast_id uuid NOT NULL REFERENCES broadcasts(id), user_id uuid NOT NULL REFERENCES users(id),
 read_at timestamptz, PRIMARY KEY(broadcast_id,user_id)
);
-- +goose Down
DROP TABLE broadcast_recipients,broadcasts,reports,segment_requests,segment_members,segments,audit_logs,events,reactions,message_requests,messages,channel_members,channels,refresh_credentials,sessions,users;
