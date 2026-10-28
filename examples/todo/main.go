package main

import (
	"log"
	"maps"
	"net/http"
	"slices"
	"sync"

	"github.com/jackielii/structpages"
)

type Todo struct {
	ID        int
	Text      string
	Completed bool
}

var (
	todoStore = make(map[int]*Todo)
	nextID    = 1
	mu        sync.RWMutex
)

func addTodo(text string) *Todo {
	mu.Lock()
	defer mu.Unlock()
	todo := &Todo{
		ID:        nextID,
		Text:      text,
		Completed: false,
	}
	todoStore[nextID] = todo
	nextID++
	return todo
}

func getTodos() []*Todo {
	mu.RLock()
	defer mu.RUnlock()
	todos := make([]*Todo, 0, len(todoStore))
	keys := maps.Keys(todoStore)
	for _, key := range slices.Backward(slices.Sorted(keys)) {
		todos = append(todos, todoStore[key])
	}
	return todos
}

func toggleTodo(id int) {
	mu.Lock()
	defer mu.Unlock()
	if todo, exists := todoStore[id]; exists {
		todo.Completed = !todo.Completed
	}
}

func removeTodo(id int) {
	mu.Lock()
	defer mu.Unlock()
	delete(todoStore, id)
}

// handler builds the app: the pages mounted on their own mux (mux.go, or
// mux_tinygo.go under TinyGo), with some sample todos. main serves it
// (serve.go, or serve_js.go in a Worker); check_test.go calls it directly.
func handler() http.Handler {
	// Add some sample todos
	addTodo("Learn Go")
	addTodo("Build a TODO app")
	addTodo("Deploy to production")

	mux := newMux()
	sp, err := structpages.Mount(mux, index{}, "/", "index",
		// htmx 2.x: route HX-Target to the page's partials.
		structpages.WithTargetSelector(structpages.HTMXRenderTarget),
		structpages.WithErrorHandler(errorHandler),
	)
	if err != nil {
		log.Fatalf("Failed to mount pages: %v", err)
	}
	_ = sp // sp provides URLFor and IDFor methods
	return mux
}

func main() { serve(handler()) }

func errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("Error: %v", err)
	if r.Header.Get("Hx-Request") == "true" {
		ErrorComp(err).Render(r.Context(), w)
		return
	}
	ErrorPage(err).Render(r.Context(), w)
}
