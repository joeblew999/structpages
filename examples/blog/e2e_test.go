package main

import (
	"testing"
	"time"

	"github.com/jackielii/structpages/e2e"
	"github.com/jackielii/structpages/examples/blog/store"
)

// repeatable fixes the store's clock, the time when served, so the demo
// posts' dates, and every answer, are the same every run.
func repeatable() {
	store.Now = func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }
}

// TestE2E crawls the app from / and compares every response with
// testdata/e2e.generated.txt, recorded from the dynamic build, so the app
// must answer the same all three ways: dynamic, static and static-worker
// (mise run example:blog:test).
func TestE2E(t *testing.T) {
	repeatable()
	e2e.Crawl(t, handler(), "testdata/e2e.generated.txt")
}

// BenchmarkE2E times the recorded GETs, to compare the three ways
// (mise run example:blog:bench).
func BenchmarkE2E(b *testing.B) {
	repeatable()
	e2e.Bench(b, handler(), "testdata/e2e.generated.txt")
}
