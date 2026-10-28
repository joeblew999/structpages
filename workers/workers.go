// Package workers runs a structpages app on the standard toolchain and, built
// with TinyGo as WebAssembly, as a Cloudflare Worker (or in workerd locally),
// from the same main:
//
//	mux := workers.NewMux()
//	structpages.Mount(mux, index{}, "/", "App")
//	workers.Serve(":8080", mux)
//
// In the reflect build that is net/http's ServeMux and ListenAndServe:
// nothing here needs TinyGo or workerd. In the two builds that read generated
// code, TinyGo and the standard toolchain with -tags structpages_tables,
// NewMux is servemux, Go's own ServeMux (TinyGo's net/http can't route Go
// 1.22 patterns), so they differ only in their compiler. In a Worker, Serve
// hands the app the requests workerd delivers, one Go runtime serving all of
// them. A Worker also needs JavaScript glue next to its app.wasm:
// tools/workers' structpages-workers command writes it.
package workers

import "net/http"

// Mux is what NewMux returns: something structpages.Mount can register on,
// that serves what it registered.
type Mux interface {
	Handle(pattern string, handler http.Handler)
	http.Handler
}
