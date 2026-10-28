//go:build go1.26 && !tinygo

package servemux

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSameAsNetHTTP sends the same requests to this ServeMux and to
// net/http's, with the same patterns, and wants the same answers: status,
// redirect, Allow header, body, and the pattern and path values the handler
// sees. It runs on Go 1.26 and later, whose net/http this package copies
// (Go 1.24's redirects with 301, not 307), and not under TinyGo, whose
// net/http mux is Go 1.21's.
func TestSameAsNetHTTP(t *testing.T) {
	patterns := []string{
		"/{$}", "GET /a", "POST /a", "/b/", "/b/{x}", "/c/{rest...}",
		"GET /d/{id}/edit", "/e/{$}", "example.com/host", "DELETE /f",
		"/items/{id}", "GET /g/{x}/h", "/admin", "/admin/{$}", "PUT /admin/users/{id}",
	}
	names := []string{"x", "rest", "id"}
	statuses := map[int]bool{}
	ours, theirs := NewServeMux(), http.NewServeMux()
	for _, p := range patterns {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s pattern=%q", p, r.Pattern)
			for _, n := range names {
				if v := r.PathValue(n); v != "" {
					fmt.Fprintf(w, " %s=%q", n, v)
				}
			}
		})
		ours.Handle(p, h)
		theirs.Handle(p, h)
	}
	for _, method := range []string{"GET", "HEAD", "POST", "PUT", "DELETE", "CONNECT"} {
		for _, target := range []string{
			"/", "/a", "/a/", "/b", "/b/", "/b/1", "/b/1/2", "/c", "/c/", "/c/x/y/z",
			"/d/42/edit", "/d/a%2Fb/edit", "/items/a%26b%2Fc%3Fd", "/items/%41", "//a",
			"/a/../b", "/b/./1", "/nope", "/e", "/e/", "/f", "/g/1/h", "/g/1/h/",
			"/admin", "/admin/", "/admin/users/7", "/a?q=1", "http://example.com/host",
		} {
			req := func() *http.Request {
				r := httptest.NewRequest(method, target, nil)
				if method == "CONNECT" {
					r.URL.Path = strings.SplitN(target, "?", 2)[0]
				}
				return r
			}
			got, want := httptest.NewRecorder(), httptest.NewRecorder()
			ours.ServeHTTP(got, req())
			theirs.ServeHTTP(want, req())
			statuses[want.Code] = true
			for _, c := range []struct{ what, got, want string }{
				{"status", fmt.Sprint(got.Code), fmt.Sprint(want.Code)},
				{"Location", got.Header().Get("Location"), want.Header().Get("Location")},
				{"Allow", got.Header().Get("Allow"), want.Header().Get("Allow")},
				{"body", got.Body.String(), want.Body.String()},
			} {
				if c.got != c.want {
					t.Errorf("%s %s: %s %q, net/http %q", method, target, c.what, c.got, c.want)
				}
			}
		}
	}
	for _, code := range []int{200, 307, 404, 405} {
		if !statuses[code] {
			t.Errorf("no request answered %d: the cases don't cover it", code)
		}
	}
}
