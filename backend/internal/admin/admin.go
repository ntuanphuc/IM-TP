package admin

import (
	"net/http"
	"strings"
	"time"

	"chattp/internal/auth"
	"chattp/internal/chat"
	"chattp/internal/platform"
	"chattp/internal/policy"
)

type Service struct{ *platform.Services }

var lists = map[string]string{
	"users":     `SELECT id,username,display_name,disabled,ad_disabled,student,teacher,admin FROM users ORDER BY username LIMIT 100`,
	"segments":  `SELECT id,key,name,sensitive FROM segments ORDER BY key LIMIT 100`,
	"audit":     `SELECT id,action,target,actor_id,created_at FROM audit_logs ORDER BY id DESC LIMIT 100`,
	"reports":   `SELECT id,message_id,reporter_id,reason,created_at FROM reports ORDER BY id DESC LIMIT 100`,
	"retention": `SELECT scope id,scope,keep_days,updated_at FROM retention_rules ORDER BY scope`,
	"policy-rules": `SELECT p.id,p.action,p.effect,p.enabled,p.subject_segment,p.object_segment,
	  s.name subject,o.name object FROM policy_rules p LEFT JOIN segments s ON s.id=p.subject_segment
	  LEFT JOIN segments o ON o.id=p.object_segment ORDER BY p.created_at DESC LIMIT 100`,
	"requests": `SELECT r.id,r.segment_id,r.user_id,r.role,r.status,r.requester_id,s.name segment_name,u.username
 FROM segment_requests r JOIN segments s ON s.id=r.segment_id JOIN users u ON u.id=r.user_id ORDER BY r.created_at DESC LIMIT 100`,
	"broadcasts": `SELECT b.id,b.body,b.status,b.scheduled_at,s.name segment_name,
 (SELECT count(*) FROM broadcast_recipients br WHERE br.broadcast_id=b.id) recipients,
 (SELECT count(*) FROM broadcast_recipients br WHERE br.broadcast_id=b.id AND br.read_at IS NOT NULL) readers
 FROM broadcasts b JOIN segments s ON s.id=b.segment_id ORDER BY b.scheduled_at DESC LIMIT 100`,
}

func (s Service) List(w http.ResponseWriter, r *http.Request, u auth.User) {
	view := chat.Param(r, "view")
	query, ok := lists[view]
	if !ok {
		platform.Error(w, 404, "not_found")
		return
	}
	if view == "users" {
		query = `SELECT id,username,display_name,disabled,ad_disabled,student,teacher,admin FROM users
  WHERE username ILIKE '%'||$1||'%' OR display_name ILIKE '%'||$1||'%' ORDER BY username LIMIT 100`
		chat.Rows(w, r, s.DB, "SELECT coalesce(jsonb_agg(to_jsonb(x)),'[]') FROM ("+query+") x", r.URL.Query().Get("q"))
		return
	}
	chat.Rows(w, r, s.DB, "SELECT coalesce(jsonb_agg(to_jsonb(x)),'[]') FROM ("+query+") x")
}

func (s Service) Members(w http.ResponseWriter, r *http.Request, u auth.User) {
	chat.Rows(w, r, s.DB, `SELECT coalesce(jsonb_agg(to_jsonb(x)),'[]') FROM (
 SELECT sm.user_id,u.username,u.display_name,sm.role FROM segment_members sm JOIN users u ON u.id=sm.user_id
 WHERE sm.segment_id::text=$1 ORDER BY u.username LIMIT 100) x`, chat.Param(r, "segment"))
}

func (s Service) Mutate(w http.ResponseWriter, r *http.Request, u auth.User) {
	ctx := r.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		chat.Failure(w, err)
		return
	}
	defer tx.Rollback(ctx)
	action, target := "", ""
	switch {
	case r.Method == http.MethodDelete && chat.Param(r, "rule") != "":
		target = chat.Param(r, "rule")
		_, err = tx.Exec(ctx, "DELETE FROM policy_rules WHERE id::text=$1", target)
		action = "policy.delete"
	case strings.HasSuffix(r.URL.Path, "/policy-rules") || chat.Param(r, "rule") != "":
		var in struct {
			Subject string `json:"subject_segment"`
			Object  string `json:"object_segment"`
			Action  string `json:"action"`
			Effect  string `json:"effect"`
			Enabled bool   `json:"enabled"`
		}
		if !platform.Decode(w, r, &in) {
			return
		}
		if !policy.Supported(in.Action) || (in.Effect != "allow" && in.Effect != "deny") {
			platform.Error(w, 400, "invalid_policy_rule")
			return
		}
		target = chat.Param(r, "rule")
		if target == "" {
			err = tx.QueryRow(ctx, `INSERT INTO policy_rules(subject_segment,object_segment,action,effect,enabled)
			   VALUES(nullif($1,'')::uuid,nullif($2,'')::uuid,$3,$4,$5) RETURNING id`, in.Subject, in.Object, in.Action, in.Effect, in.Enabled).Scan(&target)
			action = "policy.create"
		} else {
			_, err = tx.Exec(ctx, `UPDATE policy_rules SET subject_segment=nullif($2,'')::uuid,object_segment=nullif($3,'')::uuid,
			   action=$4,effect=$5,enabled=$6 WHERE id::text=$1`, target, in.Subject, in.Object, in.Action, in.Effect, in.Enabled)
			action = "policy.update"
		}
	case strings.HasSuffix(r.URL.Path, "/retention"):
		var in struct {
			Scope    string `json:"scope"`
			KeepDays int    `json:"keep_days"`
		}
		if !platform.Decode(w, r, &in) {
			return
		}
		switch in.Scope {
		case "global", "private", "dm", "group_dm", "announcement":
		default:
			platform.Error(w, 400, "invalid_retention_scope")
			return
		}
		if in.KeepDays < 1 || in.KeepDays > 3650 {
			platform.Error(w, 400, "invalid_keep_days")
			return
		}
		_, err = tx.Exec(ctx, `INSERT INTO retention_rules(scope,keep_days) VALUES($1,$2)
		    ON CONFLICT(scope) DO UPDATE SET keep_days=EXCLUDED.keep_days,updated_at=now()`, in.Scope, in.KeepDays)
		action, target = "retention.set", in.Scope
	case strings.HasSuffix(r.URL.Path, "/disable"):
		target = chat.Param(r, "user")
		if target == u.ID {
			platform.Error(w, 400, "cannot_disable_self")
			return
		}
		_, err = tx.Exec(ctx, "UPDATE users SET disabled=true WHERE id::text=$1 AND NOT admin", target)
		if err == nil {
			_, err = tx.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE user_id::text=$1 AND revoked_at IS NULL AND EXISTS(SELECT 1 FROM users WHERE id::text=$1 AND disabled)", target)
		}
		if err == nil {
			_, err = tx.Exec(ctx, "DELETE FROM segment_members WHERE user_id::text=$1 AND EXISTS(SELECT 1 FROM users WHERE id::text=$1 AND disabled)", target)
		}
		action = "user.disable"
	case strings.HasSuffix(r.URL.Path, "/segments"):
		var in struct {
			Key       string `json:"key"`
			Name      string `json:"name"`
			Sensitive bool   `json:"sensitive"`
		}
		if !platform.Decode(w, r, &in) {
			return
		}
		in.Key, in.Name = strings.TrimSpace(in.Key), strings.TrimSpace(in.Name)
		if in.Key == "" || in.Name == "" || len(in.Key) > 100 || len(in.Name) > 150 {
			platform.Error(w, 400, "invalid_segment")
			return
		}
		err = tx.QueryRow(ctx, "INSERT INTO segments(key,name,sensitive) VALUES($1,$2,$3) RETURNING id", in.Key, in.Name, in.Sensitive).Scan(&target)
		action = "segment.create"
	case strings.HasSuffix(r.URL.Path, "/members"):
		var in struct {
			User string `json:"user_id"`
			Role string `json:"role"`
		}
		if !platform.Decode(w, r, &in) {
			return
		}
		if in.Role != "member" && in.Role != "manager" && in.Role != "owner" && in.Role != "remove" {
			platform.Error(w, 400, "invalid_role")
			return
		}
		segment := chat.Param(r, "segment")
		var sensitive bool
		if err = tx.QueryRow(ctx, "SELECT sensitive FROM segments WHERE id::text=$1 FOR UPDATE", segment).Scan(&sensitive); err != nil {
			chat.Failure(w, err)
			return
		}
		var active bool
		err = tx.QueryRow(ctx, "SELECT NOT disabled AND NOT ad_disabled FROM users WHERE id::text=$1", in.User).Scan(&active)
		if err != nil || (!active && in.Role != "remove") {
			platform.Error(w, 400, "inactive_user")
			return
		}
		target = segment + "/" + in.User
		if sensitive {
			err = tx.QueryRow(ctx, "INSERT INTO segment_requests(segment_id,user_id,requester_id,role) VALUES($1,$2,$3,$4) RETURNING id", segment, in.User, u.ID, in.Role).Scan(&target)
			action = "segment.request"
		} else if in.Role == "remove" {
			_, err = tx.Exec(ctx, "DELETE FROM segment_members WHERE segment_id=$1 AND user_id=$2", segment, in.User)
			action = "segment.member_remove"
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO segment_members VALUES($1,$2,$3)
    ON CONFLICT(segment_id,user_id) DO UPDATE SET role=EXCLUDED.role`, segment, in.User, in.Role)
			action = "segment.member_set"
		}
	case chat.Param(r, "decision") != "":
		target = chat.Param(r, "request")
		decision := chat.Param(r, "decision")
		if decision != "approve" && decision != "reject" {
			platform.Error(w, 400, "invalid_decision")
			return
		}
		var segment, user, requester, role, status string
		err = tx.QueryRow(ctx, `SELECT segment_id,user_id,requester_id,role,status FROM segment_requests
   WHERE id::text=$1 FOR UPDATE`, target).Scan(&segment, &user, &requester, &role, &status)
		if err != nil {
			chat.Failure(w, err)
			return
		}
		if requester == u.ID || status != "pending" {
			platform.Error(w, 403, "decision_denied")
			return
		}
		if decision == "approve" {
			if role == "remove" {
				_, err = tx.Exec(ctx, "DELETE FROM segment_members WHERE segment_id=$1 AND user_id=$2", segment, user)
			} else {
				tag, writeErr := tx.Exec(ctx, `INSERT INTO segment_members(segment_id,user_id,role)
     SELECT $1,id,$3 FROM users WHERE id=$2 AND NOT disabled AND NOT ad_disabled
     ON CONFLICT(segment_id,user_id) DO UPDATE SET role=EXCLUDED.role`, segment, user, role)
				err = writeErr
				if err == nil && tag.RowsAffected() == 0 {
					platform.Error(w, 409, "inactive_user")
					return
				}
			}
		}
		if err == nil {
			status := "rejected"
			if decision == "approve" {
				status = "approved"
			}
			_, err = tx.Exec(ctx, "UPDATE segment_requests SET status=$2,approver_id=$3 WHERE id=$1", target, status, u.ID)
		}
		action = "segment." + decision
	case strings.HasSuffix(r.URL.Path, "/cancel"):
		target = chat.Param(r, "broadcast")
		tag, writeErr := tx.Exec(ctx, "UPDATE broadcasts SET status='cancelled' WHERE id::text=$1 AND status='scheduled'", target)
		err = writeErr
		if err == nil && tag.RowsAffected() == 0 {
			platform.Error(w, 409, "cannot_cancel")
			return
		}
		action = "broadcast.cancel"
	case strings.HasSuffix(r.URL.Path, "/broadcasts"):
		var in struct {
			Segment   string     `json:"segment_id"`
			Body      string     `json:"body"`
			Scheduled *time.Time `json:"scheduled_at"`
		}
		if !platform.Decode(w, r, &in) {
			return
		}
		if strings.TrimSpace(in.Body) == "" || len(in.Body) > 16000 {
			platform.Error(w, 400, "invalid_broadcast")
			return
		}
		allowed, checkErr := policy.Allowed(ctx, tx, u.ID, "broadcast.send", nil, in.Segment, true)
		if checkErr != nil {
			chat.Failure(w, checkErr)
			return
		}
		if !allowed {
			platform.Error(w, 403, "broadcast_denied")
			return
		}
		err = tx.QueryRow(ctx, `INSERT INTO broadcasts(actor_id,segment_id,body,scheduled_at)
   SELECT $1,id,$3,coalesce($4::timestamptz,now()) FROM segments WHERE id::text=$2 AND NOT sensitive RETURNING id`, u.ID, in.Segment, in.Body, in.Scheduled).Scan(&target)
		action = "broadcast.schedule"
	default:
		platform.Error(w, 404, "not_found")
		return
	}
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO audit_logs(actor_id,action,target) VALUES($1,$2,$3)", u.ID, action, target)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		chat.Failure(w, err)
		return
	}
	platform.JSON(w, 200, map[string]string{"id": target})
}

func (s Service) Announcements(w http.ResponseWriter, r *http.Request, u auth.User) {
	chat.Rows(w, r, s.DB, `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.scheduled_at DESC),'[]') FROM (
 SELECT b.id,b.body,b.scheduled_at,br.read_at FROM broadcast_recipients br JOIN broadcasts b ON b.id=br.broadcast_id
 WHERE br.user_id=$1 AND b.status='done' ORDER BY b.scheduled_at DESC LIMIT 100) x`, u.ID)
}

func (s Service) ReadAnnouncement(w http.ResponseWriter, r *http.Request, u auth.User) {
	tag, err := s.DB.Exec(r.Context(), "UPDATE broadcast_recipients SET read_at=coalesce(read_at,now()) WHERE broadcast_id::text=$1 AND user_id=$2", chat.Param(r, "broadcast"), u.ID)
	if err != nil {
		chat.Failure(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		platform.Error(w, 404, "not_found")
		return
	}
	platform.JSON(w, 204, nil)
}
