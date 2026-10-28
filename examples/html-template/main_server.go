package main

import (
	"log"
	"net/http"
)

// main serves the app on :8080. html-template has no Worker main, as todo's
// main_worker.go is: Go's html/template calls parts of reflect TinyGo lacks
// ((reflect.Type).NumOut), so it can't run as a Worker.
func main() {
	h := handler()
	log.Println("Listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", h))
}
