-- +goose Up
CREATE TABLE policy_rules (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 subject_segment uuid REFERENCES segments(id),
 object_segment uuid REFERENCES segments(id),
 action text NOT NULL CHECK(action IN ('dm.create','group_dm.create','channel.create','message.send','broadcast.send')),
 effect text NOT NULL CHECK(effect IN ('allow','deny')),
 enabled boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX policy_rules_enabled_action ON policy_rules(action) WHERE enabled;
-- +goose Down
DROP TABLE policy_rules;
