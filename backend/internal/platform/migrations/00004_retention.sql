-- +goose Up
CREATE TABLE retention_rules (
 scope text PRIMARY KEY CHECK(scope IN ('global','private','dm','group_dm','announcement')),
 keep_days integer NOT NULL CHECK(keep_days BETWEEN 1 AND 3650),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE file_deletions (file_id text PRIMARY KEY REFERENCES files(id), created_at timestamptz NOT NULL DEFAULT now());
ALTER TABLE files DROP CONSTRAINT files_status_check;
ALTER TABLE files ADD CONSTRAINT files_status_check CHECK(status IN ('pending','clean','infected','expired'));
-- +goose Down
DELETE FROM files WHERE status='expired' AND NOT EXISTS (SELECT 1 FROM file_deletions WHERE file_id=files.id);
UPDATE files SET status='infected' WHERE status='expired';
ALTER TABLE files DROP CONSTRAINT files_status_check;
ALTER TABLE files ADD CONSTRAINT files_status_check CHECK(status IN ('pending','clean','infected'));
DROP TABLE file_deletions,retention_rules;
