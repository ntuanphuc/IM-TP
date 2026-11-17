package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"chattp/internal/auth"
	"chattp/internal/platform"
	"chattp/internal/policy"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

type MessageIndex interface {
	Match(context.Context, string, string) ([]string, error)
}

type Service struct {
	*platform.Services
	Index MessageIndex
}
type Handler func(http.ResponseWriter, *http.Request, auth.User)

func ID() string                               { return ulid.Make().String() }
func Param(r *http.Request, key string) string { return chi.URLParam(r, key) }
func Failure(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		platform.Error(w, 404, "not_found")
		return
	}
	platform.Error(w, 503, "operation_failed")
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func Rows(w http.ResponseWriter, r *http.Request, db querier, query string, args ...any) {
	var raw json.RawMessage
	if err := db.QueryRow(r.Context(), query, args...).Scan(&raw); err != nil {
		Failure(w, err)
		return
	}
	platform.JSON(w, 200, raw)
}

func CanRead(ctx context.Context, db querier, channel, user string) bool {
	var ok bool
	err := db.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM channel_members cm JOIN users u ON u.id=cm.user_id
 WHERE cm.channel_id=$1 AND cm.user_id=$2 AND NOT u.disabled AND NOT u.ad_disabled)`, channel, user).Scan(&ok)
	return err == nil && ok
}

func (s Service) Channels(w http.ResponseWriter, r *http.Request, u auth.User) {
	Rows(w, r, s.DB, `SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY coalesce(c.preview_at,c.created_at) DESC,c.id DESC),'[]') FROM (
 SELECT ch.id,coalesce(peer.display_name,ch.name) AS name,ch.kind,ch.created_at,ch.archived_at,ch.last_seq,cm.last_read_seq,
 latest.preview,latest.created_at preview_at,
 (SELECT count(*) FROM messages m WHERE m.channel_id=ch.id AND m.deleted_at IS NULL AND m.seq>cm.last_read_seq AND m.sender_id<>$1) unread
 FROM channel_members cm JOIN channels ch ON ch.id=cm.channel_id
 LEFT JOIN LATERAL (
  SELECT u.display_name FROM channel_members p JOIN users u ON u.id=p.user_id
  WHERE ch.kind='dm' AND p.channel_id=ch.id AND p.user_id<>cm.user_id LIMIT 1
 ) peer ON true
 LEFT JOIN LATERAL (
  SELECT left(m.body,150) preview,m.created_at FROM messages m
  WHERE m.channel_id=ch.id AND m.deleted_at IS NULL ORDER BY m.seq DESC LIMIT 1
 ) latest ON true
 WHERE cm.user_id=$1
 ) c`, u.ID)
}

func (s Service) Directory(w http.ResponseWriter, r *http.Request, u auth.User) {
	Rows(w, r, s.DB, `SELECT coalesce(jsonb_agg(to_jsonb(d)),'[]') FROM (SELECT id,username,display_name,teacher
 FROM users WHERE NOT disabled AND NOT ad_disabled
 AND (ad_guid IS NOT NULL OR (NOT admin AND username IN ('chat-demo-01','chat-demo-02'))) AND id<>$1
 AND (display_name ILIKE '%'||$2||'%' OR username ILIKE '%'||$2||'%')
 ORDER BY display_name,id LIMIT 50) d`, u.ID, r.URL.Query().Get("q"))
}

func (s Service) Create(w http.ResponseWriter, r *http.Request, u auth.User) {
	var in struct {
		Name    string   `json:"name"`
		UserIDs []string `json:"user_ids"`
	}
	if !platform.Decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	isDM := r.URL.Path == "/v1/dms"
	var student bool
	ctx := r.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		Failure(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, "SELECT student FROM users WHERE id=$1", u.ID).Scan(&student); err != nil {
		Failure(w, err)
		return
	}
	if (!isDM && student) || (isDM && len(in.UserIDs) == 0) || len(in.UserIDs) > 30 || len(in.Name) > 120 {
		platform.Error(w, 403, "channel_policy_denied")
		return
	}
	kind := "private"
	people := map[string]bool{u.ID: true}
	students, teachers, staff := 0, 0, 0
	people = map[string]bool{}
	for _, id := range append(in.UserIDs, u.ID) {
		if people[id] {
			continue
		}
		people[id] = true
		var minor, teacher bool
		if err = tx.QueryRow(ctx, "SELECT student,teacher FROM users WHERE id::text=$1 AND NOT disabled AND NOT ad_disabled", id).Scan(&minor, &teacher); err != nil {
			Failure(w, err)
			return
		}
		if minor {
			students++
		} else if teacher {
			teachers++
		} else {
			staff++
		}
	}
	var key any
	defaultAllow := true
	if isDM {
		if len(people) < 2 {
			platform.Error(w, 400, "select_recipients")
			return
		}
		if len(people) == 2 {
			kind = "dm"
			if students > 1 || (students == 1 && teachers != 1) {
				platform.Error(w, 403, "dm_policy_denied")
				return
			}
			defaultAllow = students > 0 || teachers == 0 || staff == 0
		} else {
			kind = "group_dm"
			if students > 0 && teachers == 0 {
				platform.Error(w, 403, "teacher_required")
				return
			}
		}
		ids := make([]string, 0, len(people))
		names := make([]string, 0, len(people))
		for id := range people {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		key = strings.Join(ids, ":")
		for _, id := range ids {
			var name string
			if err = tx.QueryRow(ctx, "SELECT display_name FROM users WHERE id=$1", id).Scan(&name); err != nil {
				Failure(w, err)
				return
			}
			names = append(names, name)
		}
		if in.Name == "" {
			in.Name = strings.Join(names, ", ")
		}
	}
	if in.Name == "" {
		platform.Error(w, 400, "name_required")
		return
	}
	action := "channel.create"
	if isDM {
		action = kind + ".create"
	}
	targets := make([]string, 0, len(people)-1)
	for person := range people {
		if person != u.ID {
			targets = append(targets, person)
		}
	}
	allowed, err := policy.Allowed(ctx, tx, u.ID, action, targets, "", defaultAllow)
	if err != nil {
		Failure(w, err)
		return
	}
	if !allowed {
		platform.Error(w, 403, "channel_policy_denied")
		return
	}
	id := ID()
	err = tx.QueryRow(ctx, `INSERT INTO channels(id,name,kind,dm_key,owner_id) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(dm_key) DO UPDATE SET dm_key=EXCLUDED.dm_key RETURNING id`, id, in.Name, kind, key, u.ID).Scan(&id)
	if err != nil {
		Failure(w, err)
		return
	}
	for user := range people {
		if _, err = tx.Exec(ctx, "INSERT INTO channel_members(channel_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING", id, user); err != nil {
			Failure(w, err)
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
		Failure(w, err)
		return
	}
	platform.JSON(w, 201, map[string]string{"id": id})
}

const messageSelect = `SELECT m.id,m.channel_id,m.seq,m.sender_id,u.display_name sender_name,m.parent_id,m.body,m.created_at,m.edited_at,
 coalesce((SELECT jsonb_agg(jsonb_build_object('id',f.id,'name',f.name,'status',f.status,'mime',f.mime))
 FROM message_files mf JOIN files f ON f.id=mf.file_id WHERE mf.message_id=m.id),'[]') files,
 coalesce((SELECT jsonb_agg(jsonb_build_object('emoji',emoji,'user_id',user_id)) FROM reactions WHERE message_id=m.id),'[]') reactions
 FROM messages m JOIN users u ON u.id=m.sender_id`

func (s Service) Messages(w http.ResponseWriter, r *http.Request, u auth.User) {
	id := Param(r, "channel")
	if !CanRead(r.Context(), s.DB, id, u.ID) {
		platform.Error(w, 404, "channel_not_found")
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before_seq"), 10, 64)
	Rows(w, r, s.DB, `SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY m.seq),'[]') FROM (`+messageSelect+`
 WHERE m.channel_id=$1 AND m.deleted_at IS NULL AND ($2::bigint=0 OR m.seq<$2) ORDER BY m.seq DESC LIMIT 50) m`, id, before)
}

func (s Service) Search(w http.ResponseWriter, r *http.Request, u auth.User) {
	term := strings.TrimSpace(r.URL.Query().Get("q"))
	if term == "" {
		platform.JSON(w, 200, []any{})
		return
	}
	if len(term) > 200 {
		platform.Error(w, 400, "search_query_too_long")
		return
	}
	if s.Index != nil {
		ids, err := s.Index.Match(r.Context(), u.ID, term)
		if err == nil {
			Rows(w, r, s.DB, `SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY array_position($2::text[],m.id)),'[]') FROM (`+messageSelect+`
 JOIN channel_members cm ON cm.channel_id=m.channel_id AND cm.user_id=$1
 WHERE m.deleted_at IS NULL AND m.id=ANY($2::text[])) m`, u.ID, ids)
			return
		}
	}
	Rows(w, r, s.DB, `SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY m.created_at DESC),'[]') FROM (`+messageSelect+`
 JOIN channel_members cm ON cm.channel_id=m.channel_id AND cm.user_id=$1
 WHERE m.deleted_at IS NULL AND m.body ILIKE '%'||$2||'%' ORDER BY m.created_at DESC LIMIT 50) m`, u.ID, term)
}

func AddEvent(ctx context.Context, tx pgx.Tx, channel, kind, id string) error {
	// Acquire before allocating the cursor so committed events are visible in cursor order.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(198501)"); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "INSERT INTO events(channel_id,kind,payload) VALUES($1,$2,jsonb_build_object('id',$3::text))", channel, kind, id)
	return err
}

func (s Service) Send(w http.ResponseWriter, r *http.Request, u auth.User) {
	var in struct {
		Body   string   `json:"body"`
		Parent string   `json:"parent_id"`
		Key    string   `json:"client_msg_id"`
		Files  []string `json:"attachment_ids"`
	}
	if !platform.Decode(w, r, &in) {
		return
	}
	if (strings.TrimSpace(in.Body) == "" && len(in.Files) == 0) || len(in.Body) > 16000 || len(in.Key) == 0 || len(in.Key) > 128 || len(in.Files) > 5 {
		platform.Error(w, 400, "invalid_message")
		return
	}
	ctx, channel := r.Context(), Param(r, "channel")
	count, err := s.Redis.Eval(ctx, "local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],10) end; return n", []string{"send:" + u.ID}).Int()
	if err != nil {
		Failure(w, err)
		return
	}
	if count > 20 {
		platform.Error(w, 429, "message_rate_limit")
		return
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		Failure(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var kind string
	if err = tx.QueryRow(ctx, "SELECT kind FROM channels WHERE id=$1 AND archived_at IS NULL FOR UPDATE", channel).Scan(&kind); err != nil {
		Failure(w, err)
		return
	}
	if kind == "announcement" || !CanRead(ctx, tx, channel, u.ID) {
		platform.Error(w, 403, "send_denied")
		return
	}
	var targets []string
	if err = tx.QueryRow(ctx, "SELECT coalesce(array_agg(user_id::text),'{}') FROM channel_members WHERE channel_id=$1 AND user_id<>$2", channel, u.ID).Scan(&targets); err != nil {
		Failure(w, err)
		return
	}
	canSend, err := policy.Allowed(ctx, tx, u.ID, "message.send", targets, "", true)
	if err != nil {
		Failure(w, err)
		return
	}
	if !canSend {
		platform.Error(w, 403, "send_denied")
		return
	}
	if kind == "dm" || kind == "group_dm" {
		var allowed, defaultAllow bool
		err = tx.QueryRow(ctx, `SELECT (count(*)=count(*) FILTER(WHERE NOT u.disabled AND NOT u.ad_disabled))
   AND CASE WHEN $2='dm' THEN count(*)=2 AND (count(*) FILTER(WHERE u.student)=0 OR (count(*) FILTER(WHERE u.student)=1 AND count(*) FILTER(WHERE u.teacher)=1))
   ELSE count(*) FILTER(WHERE u.student)=0 OR count(*) FILTER(WHERE u.teacher)>0 END,
   $2<>'dm' OR count(*) FILTER(WHERE u.teacher)=2 OR count(*) FILTER(WHERE NOT u.teacher AND NOT u.student)=2 OR count(*) FILTER(WHERE u.student)=1
   FROM channel_members cm JOIN users u ON u.id=cm.user_id WHERE cm.channel_id=$1`, channel, kind).Scan(&allowed, &defaultAllow)
		if err != nil {
			Failure(w, err)
			return
		}
		if !allowed {
			platform.Error(w, 403, "send_denied")
			return
		}
		allowed, err = policy.Allowed(ctx, tx, u.ID, kind+".create", targets, "", defaultAllow)
		if err != nil {
			Failure(w, err)
			return
		}
		if !allowed {
			platform.Error(w, 403, "send_denied")
			return
		}
	}
	payload, _ := json.Marshal(in)
	fingerprint := auth.Digest(string(payload))
	var oldHash, id string
	var seq int64
	err = tx.QueryRow(ctx, "SELECT fingerprint,message_id,seq FROM message_requests WHERE channel_id=$1 AND sender_id=$2 AND client_msg_id=$3", channel, u.ID, in.Key).Scan(&oldHash, &id, &seq)
	if err == nil {
		if oldHash != fingerprint {
			platform.Error(w, 409, "idempotency_conflict")
			return
		}
		platform.JSON(w, 200, map[string]any{"id": id, "seq": seq})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		Failure(w, err)
		return
	}
	if in.Parent != "" {
		var ok bool
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE id=$1 AND channel_id=$2 AND deleted_at IS NULL AND parent_id IS NULL)", in.Parent, channel).Scan(&ok)
		if err != nil {
			Failure(w, err)
			return
		}
		if !ok {
			platform.Error(w, 400, "invalid_thread")
			return
		}
	}
	id = ID()
	err = tx.QueryRow(ctx, "UPDATE channels SET last_seq=last_seq+1 WHERE id=$1 RETURNING last_seq", channel).Scan(&seq)
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO messages(id,channel_id,seq,sender_id,parent_id,body) VALUES($1,$2,$3,$4,nullif($5,''),$6)", id, channel, seq, u.ID, in.Parent, in.Body)
	}
	if err == nil {
		for _, file := range in.Files {
			tag, writeErr := tx.Exec(ctx, `INSERT INTO message_files(message_id,file_id)
   SELECT $1,id FROM files WHERE id=$2 AND uploader_id=$3 AND channel_id=$4 AND status IN ('pending','clean') FOR UPDATE ON CONFLICT DO NOTHING`, id, file, u.ID, channel)
			if writeErr != nil {
				Failure(w, writeErr)
				return
			}
			if tag.RowsAffected() != 1 {
				platform.Error(w, 400, "invalid_attachment")
				return
			}
		}
	}
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO message_requests(channel_id,sender_id,client_msg_id,fingerprint,message_id,seq) VALUES($1,$2,$3,$4,$5,$6)", channel, u.ID, in.Key, fingerprint, id, seq)
	}
	if err == nil {
		err = AddEvent(ctx, tx, channel, "message.new", id)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		Failure(w, err)
		return
	}
	platform.JSON(w, 201, map[string]any{"id": id, "seq": seq})
}

func (s Service) Read(w http.ResponseWriter, r *http.Request, u auth.User) {
	var in struct {
		Seq int64 `json:"seq"`
	}
	if !platform.Decode(w, r, &in) {
		return
	}
	ctx, channel := r.Context(), Param(r, "channel")
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		Failure(w, err)
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE channel_members cm SET last_read_seq=$3 FROM channels c
 WHERE cm.user_id=$1 AND cm.channel_id=$2 AND c.id=cm.channel_id AND $3>cm.last_read_seq AND $3<=c.last_seq`, u.ID, channel, in.Seq)
	if err == nil && tag.RowsAffected() > 0 {
		err = AddEvent(ctx, tx, channel, "read.updated", channel)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		Failure(w, err)
		return
	}
	platform.JSON(w, 204, nil)
}

func (s Service) Change(w http.ResponseWriter, r *http.Request, u auth.User) {
	ctx, id := r.Context(), Param(r, "message")
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		Failure(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var channel, sender, owner string
	err = tx.QueryRow(ctx, `SELECT m.channel_id,m.sender_id,c.owner_id FROM messages m JOIN channels c ON c.id=m.channel_id
 WHERE m.id=$1 AND m.deleted_at IS NULL AND c.kind<>'announcement' AND c.archived_at IS NULL FOR UPDATE OF m`, id).Scan(&channel, &sender, &owner)
	if err != nil {
		Failure(w, err)
		return
	}
	if !CanRead(ctx, tx, channel, u.ID) {
		platform.Error(w, 404, "not_found")
		return
	}
	event := "reaction.changed"
	emoji := Param(r, "emoji")
	switch {
	case emoji != "":
		if len(emoji) > 32 {
			platform.Error(w, 400, "invalid_reaction")
			return
		}
		if r.Method == http.MethodPut {
			_, err = tx.Exec(ctx, "INSERT INTO reactions VALUES($1,$2,$3) ON CONFLICT DO NOTHING", id, u.ID, emoji)
		} else {
			_, err = tx.Exec(ctx, "DELETE FROM reactions WHERE message_id=$1 AND user_id=$2 AND emoji=$3", id, u.ID, emoji)
		}
	case strings.HasSuffix(r.URL.Path, "/report"):
		var in struct {
			Reason string `json:"reason"`
		}
		if !platform.Decode(w, r, &in) {
			return
		}
		if strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 1000 {
			platform.Error(w, 400, "invalid_reason")
			return
		}
		_, err = tx.Exec(ctx, "INSERT INTO reports(message_id,reporter_id,reason) VALUES($1,$2,$3)", id, u.ID, in.Reason)
		event = ""
	case r.Method == http.MethodPatch:
		if sender != u.ID {
			platform.Error(w, 403, "edit_denied")
			return
		}
		var in struct {
			Body string `json:"body"`
		}
		if !platform.Decode(w, r, &in) {
			return
		}
		if strings.TrimSpace(in.Body) == "" || len(in.Body) > 16000 {
			platform.Error(w, 400, "invalid_message")
			return
		}
		_, err = tx.Exec(ctx, "UPDATE messages SET body=$2,edited_at=now() WHERE id=$1", id, in.Body)
		event = "message.updated"
	case r.Method == http.MethodDelete:
		if sender != u.ID && owner != u.ID {
			platform.Error(w, 403, "delete_denied")
			return
		}
		_, err = tx.Exec(ctx, "UPDATE messages SET deleted_at=now(),body='' WHERE id=$1", id)
		if err == nil && sender != u.ID {
			_, err = tx.Exec(ctx, "INSERT INTO audit_logs(actor_id,action,target) VALUES($1,'message.moderate',$2)", u.ID, id)
		}
		event = "message.deleted"
	}
	if err == nil && event != "" {
		err = AddEvent(ctx, tx, channel, event, id)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		Failure(w, err)
		return
	}
	platform.JSON(w, 204, nil)
}

func (s Service) Thread(w http.ResponseWriter, r *http.Request, u auth.User) {
	var channel string
	id := Param(r, "message")
	err := s.DB.QueryRow(r.Context(), "SELECT channel_id FROM messages WHERE id=$1 AND deleted_at IS NULL", id).Scan(&channel)
	if err != nil || !CanRead(r.Context(), s.DB, channel, u.ID) {
		platform.Error(w, 404, "not_found")
		return
	}
	Rows(w, r, s.DB, `SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY m.seq),'[]') FROM (`+messageSelect+`
 WHERE m.channel_id=$1 AND (m.id=$2 OR m.parent_id=$2) AND m.deleted_at IS NULL ORDER BY m.seq LIMIT 100) m`, channel, id)
}

func Clean(ctx context.Context, s *platform.Services) error {
	_, err := s.DB.Exec(ctx, "DELETE FROM message_requests WHERE created_at<now()-interval '7 days'")
	return err
}
