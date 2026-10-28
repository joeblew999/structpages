//go:build !tinygo && !structpages_generated

package structpages

// routesOn is where Mount registers the routes of the mount at root: on mux
// itself, whose patterns are Go 1.22's. The builds that read generated code
// route through structpages' own copy of Go's mux instead (routes_generated.go).
func routesOn(mux Mux, _ *PageNode) Mux { return mux }
