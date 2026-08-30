// Command enshu wires config, the database pool, and the HTTP server together.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
	apphttp "github.com/Jolls/enshu/internal/http"
	"github.com/Jolls/enshu/internal/media"
)

// mediaGCGrace is how long a media file with no media_blobs row must sit untouched before the
// sweep may reclaim it (#91): bytes are written before the import transaction commits, so a
// younger file may belong to an import still running. Orders of magnitude above any real import.
const mediaGCGrace = 24 * time.Hour

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":3000"
	}
	mediaRoot := os.Getenv("MEDIA_ROOT")
	if mediaRoot == "" {
		return errors.New("MEDIA_ROOT is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	authSvc, err := auth.New(pool, auth.Config{Origin: os.Getenv("ORIGIN")})
	if err != nil {
		return fmt.Errorf("init auth: %w", err)
	}
	go authSvc.Run(ctx, time.Hour)

	blobs := media.New(mediaRoot)
	go media.NewGC(pool, blobs, mediaGCGrace).Run(ctx, time.Hour)

	handler, err := apphttp.NewHandler(pool, authSvc, blobs)
	if err != nil {
		return fmt.Errorf("build handler: %w", err)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
