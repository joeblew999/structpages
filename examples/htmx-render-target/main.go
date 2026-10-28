package main

import (
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/jackielii/structpages"
)

// PrintRoutes is a simple middleware that prints all routes to stdout
func PrintRoutes(sb *strings.Builder) structpages.MiddlewareFunc {
	fmt.Fprintln(sb, "\nRoutes:")
	fmt.Fprintln(sb, "Method\tPattern\t\tTitle")
	fmt.Fprintln(sb, "------\t-------\t\t-----")
	return func(h http.Handler, pn *structpages.PageNode) http.Handler {
		fmt.Fprintf(sb, "%s\t%- 12s\t%s\n", pn.Method, pn.FullRoute(), pn.Title)
		return h
	}
}

// intN and now make the dashboard's mock data: random, and the time.
// e2e_test.go fixes both, so every run, and every way, answers the same.
var (
	intN = rand.IntN
	now  = time.Now
)

// handler builds the app: the pages mounted on their own mux, printing its
// routes. main serves it: main_server.go on a server, main_worker.go as a
// Cloudflare Worker. e2e_test.go calls it directly.
func handler() http.Handler {
	var routes strings.Builder
	mux := http.NewServeMux()
	sp, err := structpages.Mount(mux, &dashboard{}, "/", "Dashboard",
		// htmx 2.x: route HX-Target to the page's partials.
		structpages.WithTargetSelector(structpages.HTMXRenderTarget),
		structpages.WithErrorHandler(errorHandler),
		structpages.WithMiddlewares(PrintRoutes(&routes)),
	)
	if err != nil {
		log.Fatalf("Failed to mount pages: %v", err)
	}
	_ = sp // sp provides URLFor and IDFor methods
	fmt.Println("Available routes:\n", routes.String())
	return mux
}

func errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("Error: %v", err)
	if r.Header.Get("Hx-Request") == "true" {
		ErrorComp(err).Render(r.Context(), w)
		return
	}
	ErrorPage(err).Render(r.Context(), w)
}
