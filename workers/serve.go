//go:build js && wasm

package workers

import (
	"net/http"

	"github.com/syumai/workers-go"
)

// Serve hands h the requests of the Worker it runs in, and never returns: a
// WebAssembly build can't listen on a socket, so workerd, or Cloudflare,
// delivers them. Not returning keeps the Go runtime alive, so one runtime serves every
// request the glue gives it (tools/workers' worker.mjs starts it
// once), rather than workers.Serve's, which ends after the first response.
func Serve(h http.Handler) {
	workers.ServeNonBlock(h)
	workers.Ready()
	select {}
}
