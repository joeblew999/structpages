package main

import (
	"log"
	"net/http"

	"github.com/jackielii/structpages"
)

// handler builds the app: the pages mounted on their own mux. main serves it:
// main_server.go on a server, main_worker.go as a Cloudflare Worker.
// e2e_test.go calls it directly.
func handler() http.Handler {
	mux := http.NewServeMux()
	sp, err := structpages.Mount(mux, index{}, "/", "index")
	if err != nil {
		log.Fatalf("Failed to mount pages: %v", err)
	}
	_ = sp // sp provides URLFor and IDFor methods
	return mux
}
