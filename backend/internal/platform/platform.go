package platform

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/redis/go-redis/v9"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Services struct {
	DB    *pgxpool.Pool
	Redis *redis.Client
}

func Env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func Open(ctx context.Context, migrate bool) (*Services, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	if migrate {
		database, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, err
		}
		defer database.Close()
		goose.SetBaseFS(migrations)
		if err := goose.SetDialect("postgres"); err != nil {
			return nil, err
		}
		if err := goose.UpContext(ctx, database, "migrations"); err != nil {
			return nil, err
		}
	}
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	cache := redis.NewClient(&redis.Options{Addr: Env("REDIS_ADDR", "redis:6379"), Password: os.Getenv("REDIS_PASSWORD")})
	if err = cache.Ping(ctx).Err(); err != nil {
		cache.Close()
		db.Close()
		return nil, err
	}
	return &Services{db, cache}, nil
}

func (s *Services) Close() { s.Redis.Close(); s.DB.Close() }

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}

func Error(w http.ResponseWriter, status int, code string) {
	JSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": strings.ReplaceAll(code, "_", " ")}})
}

func Decode(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		Error(w, 400, "invalid_json")
		return false
	}
	return true
}

func Run(ctx context.Context, name string, interval time.Duration, job func(context.Context) error) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			task, cancel := context.WithTimeout(ctx, interval+10*time.Second)
			err := job(task)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Error("worker task failed", "task", name, "error", err)
			}
			timer.Reset(interval)
		}
	}
}
