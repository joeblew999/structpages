//go:build !(js && wasm)

package main

import (
	"log"
	"net/http"
)

// main serves the app on :8080: every build but the Worker's.
func main() {
	h := handler()
	log.Println("Starting server on :8080")
	log.Println("Visit http://localhost:8080 to see the example")
	log.Fatal(http.ListenAndServe(":8080", h))
}
