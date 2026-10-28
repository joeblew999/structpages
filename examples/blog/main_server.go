//go:build !(js && wasm)

package main

import (
	"log"
	"net/http"
)

// main serves the app on :8080: every build but the Worker's.
func main() {
	h := handler()
	log.Println("listening on http://localhost:8080  (admin: admin/admin)")
	log.Fatal(http.ListenAndServe(":8080", h))
}
