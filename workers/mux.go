//go:build !tinygo

package workers

import "net/http"

// NewMux is net/http's ServeMux.
func NewMux() Mux { return http.NewServeMux() }
