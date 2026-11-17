package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"chattp/internal/auth"
	"chattp/internal/chat"
	"chattp/internal/files"
	"chattp/internal/jobs"
	"chattp/internal/platform"
	"chattp/internal/search"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	s, err := platform.Open(startup, false)
	if err != nil {
		return err
	}
	defer s.Close()
	directory, err := auth.DirectoryFromEnv()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	storage, err := files.New(s)
	if err != nil {
		return err
	}
	index, err := search.New(s)
	if err != nil {
		return err
	}
	start := func(name string, interval time.Duration, job func(context.Context) error) {
		wg.Add(1)
		go func() { defer wg.Done(); platform.Run(ctx, name, interval, job) }()
	}
	start("outbox", 250*time.Millisecond, func(ctx context.Context) error { return jobs.Outbox(ctx, s) })
	start("broadcast", time.Second, func(ctx context.Context) error { return jobs.Broadcast(ctx, s) })
	start("directory", 15*time.Minute, func(ctx context.Context) error { return jobs.Sync(ctx, s, directory) })
	start("file_scan", time.Second, storage.Scan)
	if index != nil {
		start("search_index", time.Second, index.Sync)
	}
	start("infected_file_cleanup", 10*time.Second, storage.PurgeInfected)
	start("retention", time.Minute, func(ctx context.Context) error { return jobs.Retention(ctx, s) })
	start("expired_file_cleanup", time.Second, storage.PurgeExpired)
	start("idempotency_cleanup", 24*time.Hour, func(ctx context.Context) error { return chat.Clean(ctx, s) })
	wg.Wait()
	return nil
}
