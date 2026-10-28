//go:build tinygo

package main

import "github.com/jackielii/structpages/servemux"

// newMux is a copy of Go 1.26's ServeMux: TinyGo's net/http routes like Go
// 1.21's, so POST /add, /toggle/{id} and the rest would never match there
// (tinygo-org/net#55).
func newMux() *servemux.ServeMux { return servemux.NewServeMux() }
