package jobs

import (
	"context"
	"errors"
	"strconv"

	"chattp/internal/auth"
	"chattp/internal/chat"
	"chattp/internal/platform"
	"chattp/internal/policy"
	"github.com/jackc/pgx/v5"
)

func Outbox(ctx context.Context, s *platform.Services) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var max int64
	err = tx.QueryRow(ctx, `SELECT coalesce(max(id),0) FROM
 (SELECT id FROM events WHERE published_at IS NULL ORDER BY id LIMIT 200 FOR UPDATE SKIP LOCKED) p`).Scan(&max)
	if err != nil || max == 0 {
		return err
	}
	if err = s.Redis.Publish(ctx, "chattp.events", strconv.FormatInt(max, 10)).Err(); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE events SET published_at=now() WHERE id<=$1 AND published_at IS NULL", max); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func Sync(ctx context.Context, s *platform.Services, d auth.Directory) error {
	if len(d.URLs) == 0 {
		return nil
	}
	accounts, err := d.List(ctx)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	guids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		guids = append(guids, a.GUID)
		if _, err = tx.Exec(ctx, `INSERT INTO users(ad_guid,username,display_name,ad_disabled,teacher,student,admin)
   VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(ad_guid) DO UPDATE SET
   username=EXCLUDED.username,display_name=EXCLUDED.display_name,ad_disabled=EXCLUDED.ad_disabled,teacher=EXCLUDED.teacher,student=EXCLUDED.student,admin=EXCLUDED.admin`,
			a.GUID, a.Username, a.Name, a.Disabled, a.Teacher, !(a.Teacher || a.Staff || a.Admin), a.Admin); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE users SET ad_disabled=true WHERE ad_guid IS NOT NULL AND NOT (ad_guid=ANY($1::text[]))", guids); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE sessions s SET revoked_at=now() FROM users u WHERE u.id=s.user_id AND (u.disabled OR u.ad_disabled) AND s.revoked_at IS NULL"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `WITH removed AS (DELETE FROM segment_members sm USING users u
 WHERE sm.user_id=u.id AND (u.disabled OR u.ad_disabled) RETURNING sm.segment_id,sm.user_id)
 INSERT INTO audit_logs(action,target) SELECT 'segment.offboard',segment_id::text||'/'||user_id::text FROM removed`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func Broadcast(ctx context.Context, s *platform.Services) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id, actor, segment, body, name string
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT b.id,b.actor_id,b.segment_id,b.body,s.name,u.admin AND NOT u.disabled AND NOT u.ad_disabled AND NOT s.sensitive
 FROM broadcasts b JOIN users u ON u.id=b.actor_id JOIN segments s ON s.id=b.segment_id
 WHERE b.status='scheduled' AND b.scheduled_at<=now() ORDER BY b.scheduled_at LIMIT 1 FOR UPDATE OF b SKIP LOCKED`).Scan(&id, &actor, &segment, &body, &name, &allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if allowed {
		allowed, err = policy.Allowed(ctx, tx, actor, "broadcast.send", nil, segment, true)
		if err != nil {
			return err
		}
	}
	if !allowed {
		if _, err = tx.Exec(ctx, "UPDATE broadcasts SET status='failed' WHERE id=$1", id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	channel, message := chat.ID(), chat.ID()
	if _, err = tx.Exec(ctx, "INSERT INTO channels(id,name,kind,owner_id,last_seq) VALUES($1,$2,'announcement',$3,1)", channel, name, actor); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO broadcast_recipients(broadcast_id,user_id)
 SELECT $1,u.id FROM segment_members sm JOIN users u ON u.id=sm.user_id WHERE sm.segment_id=$2 AND NOT u.disabled AND NOT u.ad_disabled`, id, segment); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO channel_members(channel_id,user_id) SELECT $1,user_id FROM broadcast_recipients WHERE broadcast_id=$2", channel, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO messages(id,channel_id,seq,sender_id,body) VALUES($1,$2,1,$3,$4)", message, channel, actor, body); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE broadcasts SET status='done',message_id=$2 WHERE id=$1", id, message); err != nil {
		return err
	}
	if err = chat.AddEvent(ctx, tx, channel, "broadcast.new", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
