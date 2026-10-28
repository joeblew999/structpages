//go:build !tinygo && !structpages_tables

package workers

import "net/http"

// NewMux is net/http's ServeMux, in the reflect build.
func NewMux() Mux { return http.NewServeMux() }
