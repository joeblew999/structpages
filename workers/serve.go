//go:build !(js && wasm)

package workers

import (
	"net/http"
	"time"
)

// Serve serves h on addr, as http.ListenAndServe does, giving each client 10
// seconds to send its request's headers, and returns its error.
func Serve(addr string, h http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}
