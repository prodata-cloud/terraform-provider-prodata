package client

import (
	"context"
	"testing"
	"time"
)

func TestNew_RateLimiterWiring(t *testing.T) {
	c, err := New(Config{APIBaseURL: "https://x", APIKeyID: "k", APISecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if c.limiter != nil {
		t.Error("expected no limiter when MaxRPS is 0")
	}

	c2, err := New(Config{APIBaseURL: "https://x", APIKeyID: "k", APISecretKey: "s", MaxRPS: 50})
	if err != nil {
		t.Fatal(err)
	}
	if c2.limiter == nil {
		t.Fatal("expected a limiter when MaxRPS > 0")
	}
}

func TestRateLimiter_Paces(t *testing.T) {
	l := &rateLimiter{interval: 20 * time.Millisecond}
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := l.wait(context.Background()); err != nil {
			t.Fatalf("wait: %v", err)
		}
	}
	// Three slots at 20ms apart (0, 20ms, 40ms) ⇒ total ≳ 40ms.
	if elapsed := time.Since(start); elapsed < 35*time.Millisecond {
		t.Errorf("expected pacing of ~40ms across 3 calls, got %v", elapsed)
	}
}

func TestRateLimiter_RespectsContext(t *testing.T) {
	l := &rateLimiter{interval: time.Hour}
	if err := l.wait(context.Background()); err != nil { // consume the immediate slot
		t.Fatalf("first wait: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.wait(ctx); err == nil {
		t.Error("expected a context error when the next slot is far in the future and ctx is cancelled")
	}
}
