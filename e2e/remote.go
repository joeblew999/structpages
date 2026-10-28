//go:build !tinygo

package e2e

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
		tb.Fatalf("e2e: %s: %v", URLEnv, err)
	}
	// E2E_URL is set by structpages-dev bench, to the local server it started.
	return httputil.NewSingleHostReverseProxy(u) //nolint:gosec // not user input: see above
}
