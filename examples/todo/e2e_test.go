package main

import (
	"testing"

	"github.com/jackielii/structpages/tools/e2e"
)

// TestE2E crawls the app from / and compares every response with
// testdata/e2e.generated.txt, recorded from the reflection build, so the app
// must answer the same with reflect, generated code and TinyGo
// (mise run example:todo:test).
func TestE2E(t *testing.T) { e2e.Crawl(t, handler(), "testdata/e2e.generated.txt") }

// BenchmarkE2E times the recorded GETs, to compare the three ways
// (mise run example:todo:bench).
func BenchmarkE2E(b *testing.B) { e2e.Bench(b, handler(), "testdata/e2e.generated.txt") }
