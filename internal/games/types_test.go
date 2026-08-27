package games

import (
	"context"
	"testing"
	"time"
)

func TestIGDBRateLimiterWaitContextHonorsCancellation(t *testing.T) {
	rl := NewIGDBRateLimiter()
	if err := rl.WaitContext(context.Background()); err != nil {
		t.Fatalf("first wait: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := rl.WaitContext(ctx)
	if err != context.DeadlineExceeded {
		t.Fatalf("canceled wait error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("canceled wait took %v", elapsed)
	}
}
