// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package servemux is net/http's ServeMux as of Go 1.26, for builds whose
// net/http still routes like Go 1.21's: TinyGo's (tinygo-org/net#55).
// Mount registers its routes on one in the builds that read generated code
// (TinyGo, and the standard toolchain's structpages_generated build, so the two
// differ only in their compiler); routes_generated.go in the module root says
// how, and when to delete this package.
//
// pattern.go, routing_tree.go, routing_index.go and mapping.go, and their
// tests, are Go's own files, changed only in their package clause and in
// naming http.Handler and http.NotFoundHandler.
// This file is server.go's ServeMux without the GODEBUG=httpmuxgo121 mode;
// outside net/http it records a match with Request.Pattern and SetPathValue.
package servemux

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"path"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// ServeMux is an HTTP request multiplexer with Go 1.22's patterns and
// matching. See net/http.ServeMux.
type ServeMux struct {
	mu    sync.RWMutex
	tree  routingNode
	index routingIndex
}

// NewServeMux allocates and returns a new [ServeMux].
func NewServeMux() *ServeMux {
	return &ServeMux{}
}

// cleanPath returns the canonical path for p, eliminating . and .. elements.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
	}
	np := path.Clean(p)
	// path.Clean removes trailing slash except for root;
	// put the trailing slash back if necessary.
	if p[len(p)-1] == '/' && np != "/" {
		// Fast path for common case of p being the string we want:
		if len(p) == len(np)+1 && strings.HasPrefix(p, np) {
			np = p
		} else {
			np += "/"
		}
	}
	return np
}

// stripHostPort returns h without any trailing ":<port>".
func stripHostPort(h string) string {
	// If no port on host, return unchanged
	if !strings.Contains(h, ":") {
		return h
	}
	host, _, err := net.SplitHostPort(h)
	if err != nil {
		return h // on error, return unchanged
	}
	return host
}

// findHandler finds a handler for a request.
// If there is a matching handler, it returns it and the pattern that matched.
// Otherwise it returns a Redirect or NotFound handler with the path that would match
// after the redirect.
func (mux *ServeMux) findHandler(r *http.Request) (h http.Handler, patStr string, _ *pattern, matches []string) {
	var n *routingNode
	host := r.URL.Host
	escapedPath := r.URL.EscapedPath()
	path := escapedPath
	// CONNECT requests are not canonicalized.
	if r.Method == "CONNECT" {
		// If r.URL.Path is /tree and its handler is not registered,
		// the /tree -> /tree/ redirect applies to CONNECT requests
		// but the path canonicalization does not.
		_, _, u := mux.matchOrRedirect(host, r.Method, path, r.URL)
		if u != nil {
			return http.RedirectHandler(u.String(), http.StatusTemporaryRedirect), u.Path, nil, nil
		}
		// Redo the match, this time with r.Host instead of r.URL.Host.
		// Pass a nil URL to skip the trailing-slash redirect logic.
		n, matches, _ = mux.matchOrRedirect(r.Host, r.Method, path, nil)
	} else {
		// All other requests have any port stripped and path cleaned
		// before passing to mux.handler.
		host = stripHostPort(r.Host)
		path = cleanPath(path)

		// If the given path is /tree and its handler is not registered,
		// redirect for /tree/.
		var u *url.URL
		n, matches, u = mux.matchOrRedirect(host, r.Method, path, r.URL)
		if u != nil {
			return http.RedirectHandler(u.String(), http.StatusTemporaryRedirect), n.pattern.String(), nil, nil
		}
		if path != escapedPath {
			// Redirect to cleaned path.
			patStr := ""
			if n != nil {
				patStr = n.pattern.String()
			}
			u := &url.URL{Path: path, RawQuery: r.URL.RawQuery}
			return http.RedirectHandler(u.String(), http.StatusTemporaryRedirect), patStr, nil, nil
		}
	}
	if n == nil {
		// We didn't find a match with the request method. To distinguish between
		// Not Found and Method Not Allowed, see if there is another pattern that
		// matches except for the method.
		allowedMethods := mux.matchingMethods(host, path)
		if len(allowedMethods) > 0 {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Allow", strings.Join(allowedMethods, ", "))
				http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			}), "", nil, nil
		}
		return http.NotFoundHandler(), "", nil, nil
	}
	return n.handler, n.pattern.String(), n.pattern, matches
}

// matchOrRedirect looks up a node in the tree that matches the host, method and path.
//
// If the url argument is non-nil, handler also deals with trailing-slash
// redirection: when a path doesn't match exactly, the match is tried again
// after appending "/" to the path. If that second match succeeds, the last
// return value is the URL to redirect to.
func (mux *ServeMux) matchOrRedirect(host, method, path string, u *url.URL) (_ *routingNode, matches []string, redirectTo *url.URL) {
	mux.mu.RLock()
	defer mux.mu.RUnlock()

	n, matches := mux.tree.match(host, method, path)
	// We can terminate here if any of the following is true:
	// - We have an exact match already.
	// - We were asked not to try trailing slash redirection.
	// - The URL already has a trailing slash.
	// - The URL is an empty string.
	if !exactMatch(n, path) && u != nil && !strings.HasSuffix(path, "/") && path != "" {
		// If there is an exact match with a trailing slash, then redirect.
		path += "/"
		n2, _ := mux.tree.match(host, method, path)
		if exactMatch(n2, path) {
			// It is safe to return n2 here: it is used only in the second RedirectHandler case
			// of findHandler, and that method returns before it does the "n == nil" check where
			// the first return value matters. We return it here only to make the pattern available
			// to findHandler.
			return n2, nil, &url.URL{Path: cleanPath(u.Path) + "/", RawQuery: u.RawQuery}
		}
	}
	return n, matches, nil
}

// exactMatch reports whether the node's pattern exactly matches the path.
// As a special case, if the node is nil, exactMatch return false.
//
// Before wildcards were introduced, it was clear that an exact match meant
// that the pattern and path were the same string. The only other possibility
// was that a trailing-slash pattern, like "/", matched a path longer than
// it, like "/a".
//
// With wildcards, we define an inexact match as any one where a multi wildcard
// matches a non-empty string. All other matches are exact.
// For example, these are all exact matches:
//
//	pattern   path
//	/a        /a
//	/{x}      /a
//	/a/{$}    /a/
//	/a/       /a/
//
// The last case has a multi wildcard (implicitly), but the match is exact because
// the wildcard matches the empty string.
//
// Examples of matches that are not exact:
//
//	pattern   path
//	/         /a
//	/a/{x...} /a/b
func exactMatch(n *routingNode, path string) bool {
	if n == nil {
		return false
	}
	// We can't directly implement the definition (empty match for multi
	// wildcard) because we don't record a match for anonymous multis.

	// If there is no multi, the match is exact.
	if !n.pattern.lastSegment().multi {
		return true
	}

	// If the path doesn't end in a trailing slash, then the multi match
	// is non-empty.
	if len(path) > 0 && path[len(path)-1] != '/' {
		return false
	}
	// Only patterns ending in {$} or a multi wildcard can
	// match a path with a trailing slash.
	// For the match to be exact, the number of pattern
	// segments should be the same as the number of slashes in the path.
	// E.g. "/a/b/{$}" and "/a/b/{...}" exactly match "/a/b/", but "/a/" does not.
	return len(n.pattern.segments) == strings.Count(path, "/")
}

// matchingMethods return a sorted list of all methods that would match with the given host and path.
func (mux *ServeMux) matchingMethods(host, path string) []string {
	// Hold the read lock for the entire method so that the two matches are done
	// on the same set of registered patterns.
	mux.mu.RLock()
	defer mux.mu.RUnlock()
	ms := map[string]bool{}
	mux.tree.matchingMethods(host, path, ms)
	// matchOrRedirect will try appending a trailing slash if there is no match.
	if !strings.HasSuffix(path, "/") {
		mux.tree.matchingMethods(host, path+"/", ms)
	}
	return slices.Sorted(maps.Keys(ms))
}

// ServeHTTP dispatches the request to the handler whose
// pattern most closely matches the request URL.
func (mux *ServeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.RequestURI == "*" {
		if r.ProtoAtLeast(1, 1) {
			w.Header().Set("Connection", "close")
		}
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	h, patStr, pat, matches := mux.findHandler(r)
	// net/http sets the request's unexported pattern and matches here;
	// SetPathValue gives PathValue the same answers.
	r.Pattern = patStr
	if pat != nil {
		i := 0
		for _, seg := range pat.segments {
			if seg.wild && seg.s != "" {
				r.SetPathValue(seg.s, matches[i])
				i++
			}
		}
	}
	h.ServeHTTP(w, r)
}

// Handle registers the handler for the given pattern.
// If the given pattern conflicts with one that is already registered
// or if the pattern is invalid, Handle panics.
//
// See net/http.ServeMux for details on valid patterns and conflict rules.
func (mux *ServeMux) Handle(pattern string, handler http.Handler) {
	mux.register(pattern, handler)
}

func (mux *ServeMux) register(pattern string, handler http.Handler) {
	if err := mux.registerErr(pattern, handler); err != nil {
		panic(err)
	}
}

func (mux *ServeMux) registerErr(patstr string, handler http.Handler) error {
	if patstr == "" {
		return errors.New("http: invalid pattern")
	}
	if handler == nil {
		return errors.New("http: nil handler")
	}
	if f, ok := handler.(http.HandlerFunc); ok && f == nil {
		return errors.New("http: nil handler")
	}

	pat, err := parsePattern(patstr)
	if err != nil {
		return fmt.Errorf("parsing %q: %w", patstr, err)
	}

	// Get the caller's location, for better conflict error messages.
	// Skip register and whatever calls it.
	_, file, line, ok := runtime.Caller(3)
	if !ok {
		pat.loc = "unknown location"
	} else {
		pat.loc = fmt.Sprintf("%s:%d", file, line)
	}

	mux.mu.Lock()
	defer mux.mu.Unlock()
	// Check for conflict.
	if err := mux.index.possiblyConflictingPatterns(pat, func(pat2 *pattern) error {
		if pat.conflictsWith(pat2) {
			d := describeConflict(pat, pat2)
			return fmt.Errorf("pattern %q (registered at %s) conflicts with pattern %q (registered at %s):\n%s",
				pat, pat.loc, pat2, pat2.loc, d)
		}
		return nil
	}); err != nil {
		return err
	}
	mux.tree.addPattern(pat, handler)
	mux.index.addPattern(pat)
	return nil
}

// validMethod reports whether method is a token, as net/http's does
// (through httpguts.ValidHeaderFieldName).
func validMethod(method string) bool {
	if method == "" {
		return false
	}
	for i := 0; i < len(method); i++ {
		c := method[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
			strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0) {
			return false
		}
	}
	return true
}
