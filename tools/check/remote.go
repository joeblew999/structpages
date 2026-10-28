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
	return httputil.NewSingleHostReverseProxy(u)
}
