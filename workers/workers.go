// Package workers serves a structpages app on the standard toolchain and,
// built with TinyGo as WebAssembly, as a Cloudflare Worker (or in workerd
// locally), from the same main:
//
//	mux := http.NewServeMux()
//	structpages.Mount(mux, index{}, "/", "App")
//	workers.Serve(":8080", mux)
//
// On the standard toolchain Serve is ListenAndServe: nothing here needs
// TinyGo or workerd. In a Worker, Serve hands the app the requests workerd
// delivers, one Go runtime serving all of them. A Worker also needs
// JavaScript glue next to its app.wasm: tools/workers' structpages-workers
// command writes it. (Routing needs nothing from here: under TinyGo, whose
// net/http can't route Go 1.22 patterns, Mount routes through its own copy
// of Go's ServeMux.)
package workers
