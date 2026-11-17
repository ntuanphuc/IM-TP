package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"chattp/internal/platform"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"
)

type Service struct {
	*platform.Services
	Directory Directory
	key       []byte
	origins   map[string]bool
	demo      map[string]string
}

type User struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Name      string    `json:"display_name"`
	Admin     bool      `json:"admin"`
	Session   string    `json:"-"`
	ExpiresAt time.Time `json:"-"`
}

type claims struct {
	Session string `json:"sid"`
	jwt.RegisteredClaims
}

func New(s *platform.Services, d Directory) (*Service, error) {
	key := []byte(os.Getenv("JWT_SECRET"))
	if len(key) < 32 {
		return nil, errors.New("JWT_SECRET must have at least 32 bytes")
	}
	origins := map[string]bool{}
	for _, v := range strings.Split(os.Getenv("WEB_ORIGINS"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			u, err := url.Parse(v)
			if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
				return nil, errors.New("WEB_ORIGINS must contain HTTPS origins")
			}
			if u.Port() == "443" {
				u.Host = u.Hostname()
			}
			origins[u.String()] = true
		}
	}
	demo := map[string]string{
		"chat-demo-01": os.Getenv("DEMO_USER_1_PASSWORD"),
		"chat-demo-02": os.Getenv("DEMO_USER_2_PASSWORD"),
	}
	for name, password := range demo {
		if password == "" {
			delete(demo, name)
			continue
		}
		if len(password) < 16 || len(password) > 1024 || name == adminName() {
			return nil, errors.New("invalid demo password length or admin username conflicts with demo username")
		}
		if len(d.URLs) != 0 || platform.Env("BIND_IP", "127.0.0.1") != "127.0.0.1" {
			return nil, errors.New("demo accounts require AD_URLS empty and BIND_IP=127.0.0.1")
		}
	}
	if len(demo) == 2 && demo["chat-demo-01"] == demo["chat-demo-02"] {
		return nil, errors.New("demo passwords must be different")
	}
	return &Service{Services: s, Directory: d, key: key, origins: origins, demo: demo}, nil
}

func (a *Service) OriginAllowed(r *http.Request) bool { return a.origins[r.Header.Get("Origin")] }

func (a *Service) Bootstrap(ctx context.Context) error {
	if os.Getenv("LOCAL_ADMIN_PASSWORD") != "" {
		if len(os.Getenv("LOCAL_ADMIN_PASSWORD")) < 16 {
			return errors.New("LOCAL_ADMIN_PASSWORD must have at least 16 characters")
		}
		_, err := a.DB.Exec(ctx, "INSERT INTO users(username,display_name,admin,student) VALUES($1,'Local administrator',true,false) ON CONFLICT(username) DO NOTHING", adminName())
		if err != nil {
			return err
		}
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, name := range []string{"chat-demo-01", "chat-demo-02"} {
		if name == adminName() {
			continue
		}
		if a.demo[name] != "" {
			if _, err = tx.Exec(ctx, `INSERT INTO users(username,display_name,student,teacher)
			 VALUES($1,$1,false,true) ON CONFLICT(username) DO UPDATE SET ad_disabled=false
			 WHERE users.ad_guid IS NULL AND NOT users.admin`, name); err != nil {
				return err
			}
		} else {
			if _, err = tx.Exec(ctx, `WITH off AS (UPDATE users SET ad_disabled=true
			 WHERE username=$1 AND ad_guid IS NULL AND NOT admin RETURNING id)
			 UPDATE sessions SET revoked_at=now() WHERE user_id IN (SELECT id FROM off) AND revoked_at IS NULL`, name); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func adminName() string      { return strings.ToLower(platform.Env("LOCAL_ADMIN_USER", "local-admin")) }
func Digest(v string) string { hash := sha256.Sum256([]byte(v)); return hex.EncodeToString(hash[:]) }
func opaque() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (a *Service) browser(r *http.Request) bool { return r.Header.Get("X-Chat-Client") == "web" }
func cookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: "__Host-chat_refresh", Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}

func (a *Service) issue(w http.ResponseWriter, r *http.Request, user, session, refresh string) {
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{session, jwt.RegisteredClaims{Issuer: "chattp", Subject: user, ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute))}}).SignedString(a.key)
	if err != nil {
		platform.Error(w, 500, "token_error")
		return
	}
	result := map[string]string{"access_token": token}
	if a.browser(r) {
		cookie(w, refresh, 30*24*60*60)
	} else {
		result["refresh_token"] = refresh
	}
	platform.JSON(w, 200, result)
}

func (a *Service) Login(w http.ResponseWriter, r *http.Request) {
	if a.browser(r) && !a.OriginAllowed(r) {
		platform.Error(w, 403, "origin_denied")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !platform.Decode(w, r, &in) {
		return
	}
	name := strings.ToLower(strings.TrimSpace(in.Username))
	if name == "" || len(name) > 256 || in.Password == "" || len(in.Password) > 1024 {
		platform.Error(w, 400, "invalid_credentials")
		return
	}
	ctx := r.Context()
	ip, _, _ := strings.Cut(r.RemoteAddr, ":")
	if r.Header.Get("X-Real-IP") != "" {
		ip = r.Header.Get("X-Real-IP")
	}
	for _, key := range []string{"login:ip:" + Digest(ip), "login:user:" + Digest(name)} {
		count, err := a.Redis.Eval(ctx, "local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],60) end; return n", []string{key}).Int()
		if err != nil {
			platform.Error(w, 503, "rate_limiter_unavailable")
			return
		}
		if count > 12 {
			platform.Error(w, 429, "too_many_attempts")
			return
		}
	}
	var user string
	var err error
	if name == adminName() || a.demo[name] != "" {
		admin := name == adminName()
		secret := a.demo[name]
		if admin {
			secret = os.Getenv("LOCAL_ADMIN_PASSWORD")
		}
		if secret != "" {
			salt := sha256.Sum256(append(append([]byte{}, a.key...), []byte(name)...))
			expected := argon2.IDKey([]byte(secret), salt[:], 1, 32*1024, 2, 32)
			actual := argon2.IDKey([]byte(in.Password), salt[:], 1, 32*1024, 2, 32)
			if subtle.ConstantTimeCompare(expected, actual) == 1 {
				err = a.DB.QueryRow(ctx, "SELECT id FROM users WHERE username=$1 AND ad_guid IS NULL AND admin=$2 AND NOT disabled AND NOT ad_disabled", name, admin).Scan(&user)
			}
		}
	} else {
		var account Account
		account, err = a.Directory.Verify(ctx, name, in.Password)
		if err == nil {
			err = a.DB.QueryRow(ctx, `INSERT INTO users(ad_guid,username,display_name,teacher,student,admin) VALUES($1,$2,$3,$4,$5,$6)
    ON CONFLICT(ad_guid) DO UPDATE SET username=EXCLUDED.username,display_name=EXCLUDED.display_name,teacher=EXCLUDED.teacher,student=EXCLUDED.student,admin=EXCLUDED.admin,ad_disabled=false
    WHERE NOT users.disabled RETURNING id`, account.GUID, account.Username, account.Name, account.Teacher, !(account.Teacher || account.Staff || account.Admin), account.Admin).Scan(&user)
		}
	}
	if err != nil || user == "" {
		_, _ = a.DB.Exec(ctx, "INSERT INTO audit_logs(action,target) VALUES('auth.login_failed',$1)", Digest(name))
		platform.Error(w, 401, "invalid_credentials")
		return
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		platform.Error(w, 503, "database_unavailable")
		return
	}
	defer tx.Rollback(ctx)
	var session string
	refresh := opaque()
	err = tx.QueryRow(ctx, `INSERT INTO sessions(user_id,expires_at)
  SELECT id,now()+interval '30 days' FROM users WHERE id=$1 AND NOT disabled AND NOT ad_disabled RETURNING id`, user).Scan(&session)
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO refresh_credentials(digest,session_id) VALUES($1,$2)", Digest(refresh), session)
	}
	action := "auth.login"
	if name == adminName() {
		action = "auth.break_glass"
	}
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO audit_logs(actor_id,action,target) VALUES($1,$2,$3)", user, action, session)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		platform.Error(w, 503, "login_unavailable")
		return
	}
	a.issue(w, r, user, session, refresh)
}

func (a *Service) Refresh(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Refresh string `json:"refresh_token"`
	}
	if !platform.Decode(w, r, &in) {
		return
	}
	c, cookieErr := r.Cookie("__Host-chat_refresh")
	if a.browser(r) {
		if !a.OriginAllowed(r) {
			platform.Error(w, 403, "origin_denied")
			return
		}
		if cookieErr != nil || in.Refresh != "" {
			platform.Error(w, 401, "invalid_refresh")
			return
		}
		in.Refresh = c.Value
	} else if cookieErr == nil {
		platform.Error(w, 400, "invalid_refresh")
		return
	}
	if in.Refresh == "" {
		platform.Error(w, 401, "invalid_refresh")
		return
	}
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		platform.Error(w, 503, "database_unavailable")
		return
	}
	defer tx.Rollback(ctx)
	var session, user string
	var used, active bool
	err = tx.QueryRow(ctx, `SELECT s.id,s.user_id,c.used_at IS NOT NULL,
  s.revoked_at IS NULL AND s.expires_at>now() AND NOT u.disabled AND NOT u.ad_disabled
  FROM refresh_credentials c JOIN sessions s ON s.id=c.session_id JOIN users u ON u.id=s.user_id
  WHERE c.digest=$1 FOR UPDATE OF c,s`, Digest(in.Refresh)).Scan(&session, &user, &used, &active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		platform.Error(w, 401, "invalid_refresh")
		return
	}
	if err != nil {
		platform.Error(w, 503, "refresh_unavailable")
		return
	}
	if used {
		if _, err = tx.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE id=$1", session); err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			platform.Error(w, 503, "refresh_unavailable")
			return
		}
		platform.Error(w, 401, "refresh_reused")
		return
	}
	refresh := opaque()
	_, err = tx.Exec(ctx, "UPDATE refresh_credentials SET used_at=now() WHERE digest=$1", Digest(in.Refresh))
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO refresh_credentials(digest,session_id) VALUES($1,$2)", Digest(refresh), session)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		platform.Error(w, 503, "refresh_unavailable")
		return
	}
	a.issue(w, r, user, session, refresh)
}

func (a *Service) Token(ctx context.Context, raw string) (User, error) {
	var c claims
	token, err := jwt.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) { return a.key, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("chattp"), jwt.WithExpirationRequired())
	if err != nil || !token.Valid || c.Session == "" || c.Subject == "" {
		return User{}, ErrCredentials
	}
	var user User
	user.Session = c.Session
	user.ExpiresAt = c.ExpiresAt.Time
	err = a.DB.QueryRow(ctx, `SELECT u.id,u.username,u.display_name,u.admin FROM users u JOIN sessions s ON s.user_id=u.id
  WHERE u.id=$1 AND s.id=$2 AND NOT u.disabled AND NOT u.ad_disabled AND s.revoked_at IS NULL AND s.expires_at>now()`, c.Subject, c.Session).Scan(&user.ID, &user.Username, &user.Name, &user.Admin)
	return user, err
}

func Bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (a *Service) Logout(w http.ResponseWriter, r *http.Request, user User) {
	if a.browser(r) && !a.OriginAllowed(r) {
		platform.Error(w, 403, "origin_denied")
		return
	}
	if _, err := a.DB.Exec(r.Context(), "UPDATE sessions SET revoked_at=now() WHERE id=$1", user.Session); err != nil {
		platform.Error(w, 503, "logout_unavailable")
		return
	}
	if a.browser(r) {
		cookie(w, "", -1)
	}
	platform.JSON(w, 204, nil)
}
