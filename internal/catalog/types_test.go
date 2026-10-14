package catalog

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

func TestIGDBRateLimiterPacesAtFourPerSecond(t *testing.T) {
	rl := NewIGDBRateLimiter()
	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := rl.WaitContext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// Five requests need four 250ms gaps.
	if elapsed := time.Since(start); elapsed < 950*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Errorf("5 requests took %v, want about 1s", elapsed)
	}
}

func TestIGDBRateLimiterServesInteractiveFirst(t *testing.T) {
	rl := NewIGDBRateLimiterWithInterval(40 * time.Millisecond)
	rl.WaitContext(context.Background()) // occupy the current slot
	order := make(chan string, 4)
	bg := context.Background()
	for i := 0; i < 3; i++ {
		go func() {
			rl.WaitContext(bg)
			order <- "background"
		}()
	}
	time.Sleep(10 * time.Millisecond) // the background requests queue first
	go func() {
		rl.WaitContext(WithLane(context.Background(), LaneInteractive))
		order <- "interactive"
	}()
	if first := <-order; first != "interactive" {
		t.Errorf("first served: %s, want the interactive request", first)
	}
	for i := 0; i < 3; i++ {
		<-order
	}
	st := rl.Status()
	if st[0].Lane != "interactive" || st[0].Requests != 1 || st[1].Requests != 4 {
		t.Errorf("status %+v", st)
	}
}
