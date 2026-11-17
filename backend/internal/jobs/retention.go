package jobs

import (
	"context"

	"chattp/internal/chat"
	"chattp/internal/platform"
)

func Retention(ctx context.Context, s *platform.Services) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `WITH candidates AS (
 SELECT m.id FROM messages m JOIN channels c ON c.id=m.channel_id
 LEFT JOIN retention_rules scoped ON scoped.scope=c.kind
 LEFT JOIN retention_rules global ON global.scope='global'
 WHERE (m.deleted_at IS NULL OR EXISTS(SELECT 1 FROM message_files f WHERE f.message_id=m.id)
 OR EXISTS(SELECT 1 FROM broadcasts b WHERE b.message_id=m.id AND b.status='done'))
 AND m.created_at<now()-make_interval(days=>coalesce(scoped.keep_days,global.keep_days))
 ORDER BY m.created_at LIMIT 250 FOR UPDATE OF m SKIP LOCKED
 )
 UPDATE messages m SET deleted_at=coalesce(m.deleted_at,now()),body=''
 FROM candidates c WHERE m.id=c.id RETURNING m.id,m.channel_id`)
	if err != nil {
		return err
	}
	type message struct{ id, channel string }
	var expired []message
	for rows.Next() {
		var m message
		if err = rows.Scan(&m.id, &m.channel); err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(expired) == 0 {
		return nil
	}
	for _, m := range expired {
		if _, err = tx.Exec(ctx, `WITH detached AS (DELETE FROM message_files WHERE message_id=$1 RETURNING file_id),
   expired_files AS (UPDATE files f SET status='expired' FROM detached d WHERE f.id=d.file_id RETURNING f.id)
   INSERT INTO file_deletions(file_id) SELECT id FROM expired_files ON CONFLICT DO NOTHING`, m.id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE broadcasts SET body='',status='expired' WHERE message_id=$1", m.id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM reactions WHERE message_id=$1", m.id); err != nil {
			return err
		}
		if err = chat.AddEvent(ctx, tx, m.channel, "message.deleted", m.id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
