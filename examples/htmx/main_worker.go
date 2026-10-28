//go:build js && wasm

package main

import "github.com/jackielii/structpages/workers"

// main serves the app as a Cloudflare Worker: the static-worker build, TinyGo
// WebAssembly, which can't listen on a socket. workerd delivers the requests.
func main() {
	workers.Serve(handler())
}
