package policy

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type Querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func Supported(action string) bool {
	switch action {
	case "dm.create", "group_dm.create", "channel.create", "message.send", "broadcast.send":
		return true
	default:
		return false
	}
}

// Allowed evaluates manual segment rules. The caller enforces mandatory safety checks
// independently and supplies the default decision for its operation.
func Allowed(ctx context.Context, db Querier, user, action string, targetUsers []string, targetSegment string, defaultAllow bool) (bool, error) {
	if targetUsers == nil {
		targetUsers = []string{}
	}
	var deny, allow bool
	err := db.QueryRow(ctx, `SELECT coalesce(bool_or(r.effect='deny'),false),coalesce(bool_or(r.effect='allow'),false)
 FROM policy_rules r WHERE r.enabled AND r.action=$2
 AND (r.subject_segment IS NULL OR EXISTS (
  SELECT 1 FROM segment_members sm WHERE sm.user_id=$1 AND sm.segment_id=r.subject_segment
 ))
 AND (r.object_segment IS NULL OR r.object_segment::text=$4 OR EXISTS (
  SELECT 1 FROM segment_members sm WHERE sm.user_id::text=ANY($3::text[]) AND sm.segment_id=r.object_segment
 ))`, user, action, targetUsers, targetSegment).Scan(&deny, &allow)
	return !deny && (allow || defaultAllow), err
}
