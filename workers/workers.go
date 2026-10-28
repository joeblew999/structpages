// Package workers serves a structpages app as a Cloudflare Worker (or in
// workerd locally), built with TinyGo as WebAssembly. The app keeps its
// ordinary main for every other build, and adds one for the Worker's:
//
//	//go:build js && wasm
//
//	package main
//
//	func main() { workers.Serve(handler()) }
//
// where its ordinary main, built !(js && wasm), runs
// http.ListenAndServe(":8080", handler()). In a Worker, Serve hands the app
// the requests workerd delivers, as the JavaScript glue next to its app.wasm
// gives them, each Go runtime kept for request after request: tools/workers'
// structpages-workers command builds the Worker, glue and all. (Routing needs nothing from here:
// under TinyGo, whose net/http can't route Go 1.22 patterns, Mount routes
// through its own copy of Go's ServeMux.)
package workers
