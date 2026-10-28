package structpages

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TinyGo's net/http (tinygo-org/net) has a pre-1.22 ServeMux (tinygo-org/net#55). These probes ask the mux itself
// instead of checking for TinyGo, so the tests they guard run again by themselves once it catches up.

// probeMux reports the {id} wildcard net/http's ServeMux gives pattern for a request to path, and whether it matched.
func probeMux(pattern, method, path string) (string, bool) {
	var got string
	matched := false
	mux := http.NewServeMux()
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { got, matched = r.PathValue("id"), true })
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, http.NoBody))
	return got, matched
}

// muxRoutesPatterns: Go 1.22's patterns. A method (a POST must not reach a GET route), a wildcard, and {$}, which
// matches the path exactly (so "/a/" doesn't serve "/a/b").
var muxRoutesPatterns = sync.OnceValue(func() bool {
	id, ok := probeMux("GET /probe/{id}", http.MethodGet, "/probe/x")
	_, wrongMethod := probeMux("GET /probe/{id}", http.MethodPost, "/probe/x")
	_, exact := probeMux("/probe/{$}", http.MethodGet, "/probe/")
	_, prefix := probeMux("/probe/{$}", http.MethodGet, "/probe/more")
	return ok && id == "x" && !wrongMethod && exact && !prefix
})

// muxKeepsEscapedSlash: Go matches wildcards against the escaped path, so "a%2Fb" is one segment, "a/b".
// tinygo-org/net#56 fixes methods but splits the decoded path.
var muxKeepsEscapedSlash = sync.OnceValue(func() bool {
	id, ok := probeMux("/probe/{id}", http.MethodGet, "/probe/a%2Fb")
	return ok && id == "a/b"
})

// requireMuxPatterns skips a test that needs net/http's ServeMux to route Go 1.22 patterns.
func requireMuxPatterns(t *testing.T) {
	t.Helper()
	if !muxRoutesPatterns() {
		t.Skip("net/http's ServeMux here doesn't route Go 1.22 patterns (TinyGo: tinygo-org/net#55)")
	}
}

// requireMuxEscapedSlash skips a test that needs an escaped "/" to stay inside one wildcard segment.
func requireMuxEscapedSlash(t *testing.T) {
	t.Helper()
	if !muxKeepsEscapedSlash() {
		t.Skip("net/http's ServeMux here splits an escaped / in a wildcard (TinyGo: tinygo-org/net#55, #56)")
	}
}
