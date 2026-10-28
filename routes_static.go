//go:build tinygo || structpages_static

package structpages

import (
	"reflect"
	"strings"
	"sync"

	"github.com/jackielii/structpages/internal/servemux"
)

// TinyGo's net/http comes from tinygo-org/net, whose ServeMux still routes
// like Go 1.21's: patterns with a method, a wildcard or {$} never match, and
// structpages registers its route tags as such patterns
// (https://github.com/tinygo-org/net/issues/55; the fix,
// https://github.com/tinygo-org/net/pull/56, is open). So a TinyGo build
// registers a mount's routes on internal/servemux, Go 1.26's ServeMux, and
// hands mux the mount's path with patterns a Go 1.21 mux reads as Go 1.22's
// does. The structpages_static build does the same on the standard
// toolchain, so it runs TinyGo's code path in full and only the compiler
// differs between the two.
//
// One difference from registering on mux directly: mux sees only the
// mount's prefix, so a handler of the app's own on mux that is more specific
// than that prefix, but less specific than a page's route, takes the page's
// requests (an app's /admin/ over a page's /admin/settings, when mounted at
// /), and a route mux would reject as a conflict isn't one.
//
// When a TinyGo release ships Go 1.22's ServeMux, delete this file and
// internal/servemux, and drop the build constraint from routes_dynamic.go, so
// Mount registers on mux directly again. It is safe when the suite passes
// under `tinygo test` without them: TestMountAtSubpath,
// TestMountUnderOuterMux, TestMiddlewareWithHTTPMethods and
// TestURLFor_requestValuesRoundTrip are among the tests a Go 1.21 mux fails.

// router is the servemux a mux hands its mounts to: one per mux, so mounts
// sharing a mux share one, and its patterns conflict as they would there.
type router struct {
	*servemux.ServeMux
	mu     sync.Mutex
	handed map[string]bool // patterns registered on the mux
}

var routers sync.Map // Mux → *router, for a comparable Mux

func routesOn(mux Mux, root *PageNode) Mux {
	var rt *router
	if reflect.TypeOf(mux).Comparable() {
		v, _ := routers.LoadOrStore(mux, &router{ServeMux: servemux.NewServeMux(), handed: map[string]bool{}})
		rt = v.(*router)
	} else {
		rt = &router{ServeMux: servemux.NewServeMux(), handed: map[string]bool{}}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, p := range handoffPatterns(root.FullRoute()) {
		if !rt.handed[p] {
			rt.handed[p] = true
			mux.Handle(p, rt.ServeMux)
		}
	}
	return rt.ServeMux
}

// handoffPatterns are the patterns that hand a mount at path to its router:
// its literal prefix, up to the first wildcard, as a subtree and exactly,
// so the router answers the trailing-slash redirect, not the mux.
func handoffPatterns(path string) []string {
	prefix := ""
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if seg == "" || strings.Contains(seg, "{") {
			break
		}
		prefix += "/" + seg
	}
	if prefix == "" {
		return []string{"/"}
	}
	return []string{prefix, prefix + "/"}
}
