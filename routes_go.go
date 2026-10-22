//go:build !tinygo && !structpages_router

package structpages

// routesOn is where Mount registers the routes of the mount at root: on mux
// itself, whose patterns are Go 1.22's. TinyGo builds route through
// structpages' own copy of Go's mux instead (routes_tinygo.go).
func routesOn(mux Mux, _ *PageNode) Mux { return mux }
