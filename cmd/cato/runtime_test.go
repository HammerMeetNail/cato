package main

import (
	"context"
	"testing"
	"time"
)

func TestMaintenanceCancellationJoinsActiveSweep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	done := runMaintenance(ctx, 24*time.Hour, func(ctx context.Context) { close(entered); <-ctx.Done(); <-release })
	<-entered
	cancel()
	select {
	case <-done:
		t.Fatal("returned before sweep finished")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not stop")
	}
}
