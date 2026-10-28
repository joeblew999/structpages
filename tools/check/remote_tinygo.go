//go:build tinygo

package check

import (
	"net/http"
	"testing"
)

// remote is left out of TinyGo builds: the client timing a served app runs
// on the standard toolchain, whichever way the app was built.
func remote(tb testing.TB, _ string) http.Handler {
	tb.Fatalf("check: %s: run the benchmark with go test, not tinygo test", URLEnv)
	return nil
}
