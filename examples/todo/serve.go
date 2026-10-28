//go:build !(js && wasm)

package main

import (
	"log"
	"net/http"
)

// serve serves h on :8080.
func serve(h http.Handler) {
	log.Println("Starting TODO app on :8080")
	http.ListenAndServe(":8080", h)
}
