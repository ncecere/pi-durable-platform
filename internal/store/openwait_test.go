package store

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestOpenWaitGivesUpAfterWait(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	start := time.Now()
	// Nothing listens on port 1: every attempt fails fast.
	_, err := OpenWait(context.Background(), "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1", 2500*time.Millisecond, log)
	if err == nil {
		t.Fatal("OpenWait succeeded against a closed port")
	}
	if took := time.Since(start); took < time.Second || took > 8*time.Second {
		t.Fatalf("OpenWait took %v, want it to retry for about the wait", took)
	}
}

func TestOpenWaitFailsFastOnBadURL(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	start := time.Now()
	_, err := OpenWait(context.Background(), "::not a url", time.Minute, log)
	if err == nil || !strings.HasPrefix(err.Error(), "DATABASE_URL:") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("a malformed DATABASE_URL was retried")
	}
}

func TestOpenWaitStopsOnCancel(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := OpenWait(ctx, "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1", time.Minute, log); err == nil {
		t.Fatal("OpenWait succeeded")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("OpenWait ignored cancellation")
	}
}
