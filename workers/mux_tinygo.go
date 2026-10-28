//go:build tinygo

package workers

import "github.com/jackielii/structpages/workers/servemux"

// NewMux is servemux, a copy of Go 1.26's ServeMux: TinyGo's net/http routes
// like Go 1.21's, so methods, wildcards and {$} in patterns would never match
// (tinygo-org/net#55).
func NewMux() Mux { return servemux.NewServeMux() }
