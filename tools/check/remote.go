//go:build !tinygo

package check

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"testing"
)

// remote is a handler that passes each request on to the app served at base.
func remote(tb testing.TB, base string) http.Handler {
	u, err := url.Parse(base)
	if err != nil {
		tb.Fatalf("check: %s: %v", URLEnv, err)
	}
	// CHECK_URL is set by the mise task running the benchmark, to the local server it started.
	return httputil.NewSingleHostReverseProxy(u) //nolint:gosec // not user input: see above

}
