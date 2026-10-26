package kimi

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestHookLockWaitsForCompleteDispatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.hook-lock")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err := acquireHookLock(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	entered := make(chan error, 1)
	go func() {
		unlock, err := acquireHookLock(ctx, path)
		if err == nil {
			unlock()
		}
		entered <- err
	}()
	select {
	case err := <-entered:
		t.Fatalf("second lifecycle command entered before first finished: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	release()
	if err := <-entered; err != nil {
		t.Fatal(err)
	}
}

func TestHookLockDoesNotStealLiveOrStaleOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.hook-lock")
	release, err := acquireHookLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = acquireHookLock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded wait without stealing lock, got %v", err)
	}
}
