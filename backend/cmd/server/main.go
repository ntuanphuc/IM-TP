package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"chattp/internal/admin"
	"chattp/internal/auth"
	"chattp/internal/chat"
	"chattp/internal/files"
	"chattp/internal/platform"
	"chattp/internal/realtime"
	"chattp/internal/search"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	migrate := len(os.Args) > 1 && os.Args[1] == "migrate"
	services, err := platform.Open(startup, migrate)
	if err != nil {
		return err
	}
	defer services.Close()
	directory, err := auth.DirectoryFromEnv()
	if err != nil {
		return err
	}
	identity, err := auth.New(services, directory)
	if err != nil {
		return err
	}
	storage, err := files.New(services)
	if err != nil {
		return err
	}
	if migrate {
		if err = storage.EnsureBucket(startup); err != nil {
			return err
		}
		return identity.Bootstrap(startup)
	}
	conversations := chat.Service{Services: services}
	index, err := search.New(services)
	if err != nil {
		return err
	}
	if index != nil {
		conversations.Index = index
	}
	operations := admin.Service{Services: services}
	hub := realtime.New(ctx, identity)

	secure := func(handler chat.Handler, adminOnly bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			call, cancel := context.WithTimeout(r.Context(), 75*time.Second)
			defer cancel()
			u, err := identity.Token(call, auth.Bearer(r))
			if err != nil {
				platform.Error(w, 401, "unauthorized")
				return
			}
			if adminOnly && !u.Admin {
				platform.Error(w, 403, "admin_required")
				return
			}
			handler(w, r.WithContext(call), u)
		}
	}
	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.Recoverer)
	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		check, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if services.DB.Ping(check) != nil || services.Redis.Ping(check).Err() != nil {
			platform.Error(w, 503, "unhealthy")
			return
		}
		platform.JSON(w, 200, map[string]string{"status": "ok"})
	})
	router.Route("/v1", func(r chi.Router) {
		r.Get("/ws", hub.ServeHTTP)
		r.Post("/auth/login", identity.Login)
		r.Post("/auth/refresh", identity.Refresh)
		r.Post("/auth/logout", secure(identity.Logout, false))
		r.Get("/me", secure(func(w http.ResponseWriter, r *http.Request, u auth.User) { platform.JSON(w, 200, u) }, false))
		r.Get("/users", secure(conversations.Directory, false))
		r.Get("/channels", secure(conversations.Channels, false))
		r.Post("/channels", secure(conversations.Create, false))
		r.Post("/dms", secure(conversations.Create, false))
		r.Get("/channels/{channel}/messages", secure(conversations.Messages, false))
		r.Post("/channels/{channel}/messages", secure(conversations.Send, false))
		r.Post("/channels/{channel}/read", secure(conversations.Read, false))
		r.Post("/channels/{channel}/files", secure(storage.Upload, false))
		r.Get("/files/{file}", secure(storage.Download, false))
		r.Get("/files/{file}/thumbnail", secure(storage.Thumbnail, false))
		r.Get("/messages/{message}/thread", secure(conversations.Thread, false))
		r.Patch("/messages/{message}", secure(conversations.Change, false))
		r.Delete("/messages/{message}", secure(conversations.Change, false))
		r.Post("/messages/{message}/report", secure(conversations.Change, false))
		r.Put("/messages/{message}/reactions/{emoji}", secure(conversations.Change, false))
		r.Delete("/messages/{message}/reactions/{emoji}", secure(conversations.Change, false))
		r.Get("/search", secure(conversations.Search, false))
		r.Get("/broadcasts", secure(operations.Announcements, false))
		r.Put("/broadcasts/{broadcast}/read", secure(operations.ReadAnnouncement, false))
		r.Route("/admin", func(r chi.Router) {
			r.Get("/{view}", secure(operations.List, true))
			r.Post("/users/{user}/disable", secure(operations.Mutate, true))
			r.Put("/retention", secure(operations.Mutate, true))
			r.Post("/policy-rules", secure(operations.Mutate, true))
			r.Put("/policy-rules/{rule}", secure(operations.Mutate, true))
			r.Delete("/policy-rules/{rule}", secure(operations.Mutate, true))
			r.Post("/segments", secure(operations.Mutate, true))
			r.Get("/segments/{segment}/members", secure(operations.Members, true))
			r.Post("/segments/{segment}/members", secure(operations.Mutate, true))
			r.Post("/requests/{request}/{decision}", secure(operations.Mutate, true))
			r.Post("/broadcasts", secure(operations.Mutate, true))
			r.Post("/broadcasts/{broadcast}/cancel", secure(operations.Mutate, true))
		})
	})
	httpServer := &http.Server{Addr: ":8080", Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 90 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 90 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	slog.Info("listening", "address", httpServer.Addr)
	if err = httpServer.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}
