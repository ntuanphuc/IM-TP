-- +goose Up
CREATE TABLE search_state (name text PRIMARY KEY, cursor bigint NOT NULL DEFAULT 0);
INSERT INTO search_state(name) VALUES('messages');
-- +goose Down
DROP TABLE search_state;
