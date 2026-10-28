package main

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/jackielii/structpages/e2e"
)

// repeatable fixes the dashboard's mock data, random when served, so every
// run, and every way, answers the same.
func repeatable() {
	intN = rand.New(rand.NewPCG(1, 2)).IntN
	now = func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }
}

// TestE2E crawls the app from / and compares every response with
// testdata/e2e.generated.txt, recorded from the dynamic build, so the app
// must answer the same all three ways: dynamic, static and static-worker
// (mise run example:htmx-render-target:test).
func TestE2E(t *testing.T) {
	repeatable()
	e2e.Crawl(t, handler(), "testdata/e2e.generated.txt")
}

// BenchmarkE2E times the recorded GETs, to compare the three ways
// (mise run example:htmx-render-target:bench).
func BenchmarkE2E(b *testing.B) {
	repeatable()
	e2e.Bench(b, handler(), "testdata/e2e.generated.txt")
}
