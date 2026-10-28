package main

import (
	"testing"

	"github.com/jackielii/structpages/tools/check"
)

// TestCheck crawls the app from / and compares every response with
// testdata/check.generated.txt, recorded from the reflection build, so the app
// must answer the same with reflect, generated code and TinyGo
// (mise run examples).
func TestCheck(t *testing.T) { check.Crawl(t, handler(), "testdata/check.generated.txt") }

// BenchmarkCheck times the recorded GETs, to compare the three ways
// (mise run bench:examples).
func BenchmarkCheck(b *testing.B) { check.Bench(b, handler(), "testdata/check.generated.txt") }
