//go:build tinygo || structpages_tables

package workers

import "github.com/jackielii/structpages/workers/servemux"

// NewMux is servemux, a copy of Go 1.26's ServeMux, in the two builds that
// read generated code. TinyGo needs it: its net/http routes like Go 1.21's,
// so methods, wildcards and {$} in patterns would never match
// (tinygo-org/net#55). The generated build on the standard toolchain uses it
// too, so it runs TinyGo's code path in full and only the compiler differs
// between the two.
func NewMux() Mux { return servemux.NewServeMux() }
