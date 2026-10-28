//go:build js && wasm

package main

import (
	"net/http"

	"github.com/syumai/workers-go"
)

// serve hands h the requests of the Worker it runs in: a TinyGo WebAssembly
// build can't listen on a socket, so workerd, or Cloudflare, delivers them.
func serve(h http.Handler) { workers.Serve(h) }
