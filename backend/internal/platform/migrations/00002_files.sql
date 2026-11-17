-- +goose Up
CREATE TABLE files (
 id text PRIMARY KEY, channel_id text NOT NULL REFERENCES channels(id),
 uploader_id uuid NOT NULL REFERENCES users(id), name text NOT NULL, mime text NOT NULL,
 size bigint NOT NULL, status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','clean','infected')),
 scanned_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE message_files (
 message_id text NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 file_id text NOT NULL UNIQUE REFERENCES files(id), PRIMARY KEY(message_id,file_id)
);
CREATE INDEX files_pending ON files(created_at) WHERE status='pending';
-- +goose Down
DROP TABLE message_files,files;
