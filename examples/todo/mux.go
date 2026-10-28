//go:build !tinygo

package main

import "net/http"

// newMux is net/http's ServeMux.
func newMux() *http.ServeMux { return http.NewServeMux() }
