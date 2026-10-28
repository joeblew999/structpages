package main

import (
	"testing"

	"github.com/jackielii/structpages/tools/check"
)

// TestCheck crawls the app from / and compares every response with
// testdata/check.generated.txt, recorded from the reflection build, so the app
// must answer the same with reflect, generated code and TinyGo
// (mise run check:examples).
func TestCheck(t *testing.T) { check.Crawl(t, handler(), "testdata/check.generated.txt") }
