//go:build js && wasm

package workers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"syscall/js"

	"github.com/syumai/workers-go"
)

// What tools/workers' worker.mjs and this runtime call each other through,
// and JavaScript's byte array.
var (
	binding    = js.Global().Get("context").Get("binding")
	uint8Array = js.Global().Get("Uint8Array")
)

// What this runtime has served: how many requests, the allocation counter
// after the last look, and the most one request allocated since.
var (
	served      int
	allocated   uint64
	mostRequest uint64
)

// Serve hands h the requests of the Worker it runs in, and never returns: a
// WebAssembly build can't listen on a socket, so workerd, or Cloudflare,
// delivers them, and not returning keeps the Go runtime alive for the next
// one.
//
// worker.mjs gives a request in one call, the binding's serve, as two values,
// and takes the answer in one, respond (answer has the protocol): workers-go's
// own request path costs a request a dozen goroutines and some hundred values
// crossing between JavaScript and Go, and each crossing costs. A runtime that
// worker.mjs starts while the Worker's module loads is given paths to warm up
// with, the binding's warm: Serve first answers a GET of each, into nothing,
// so what a new isolate's first request would pay for is done before it
// comes. workers-go still gets the handler, so its own request path works too
// (structpages-workers -glue workers-go).
func Serve(h http.Handler) {
	if paths := binding.Get("warm"); paths.Type() == js.TypeObject {
		for i := range paths.Length() {
			r, err := http.NewRequest(http.MethodGet, "http://warm.invalid"+paths.Index(i).String(), http.NoBody)
			if err == nil {
				h.ServeHTTP(&discard{header: http.Header{}}, r)
			}
		}
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	allocated = stats.TotalAlloc // start-up is no request's allocation
	handler := guard(h)
	binding.Set("serve", js.FuncOf(func(_ js.Value, args []js.Value) any {
		answer(handler, args[0].String(), args[1])
		return nil
	}))
	workers.ServeNonBlock(handler)
	workers.Ready()
	select {}
}

// discard is a ResponseWriter that keeps nothing.
type discard struct{ header http.Header }

func (d *discard) Header() http.Header         { return d.header }
func (d *discard) Write(p []byte) (int, error) { return len(p), nil }
func (d *discard) WriteHeader(int)             {}

// answer runs one request, in the goroutine of the call from JavaScript. head
// is the method, the URL, then each header's name and its value, one per
// line; body is the request's bytes (a Uint8Array), or null. The answer goes
// back through two functions worker.mjs puts on the binding:
//
//   - respond(status, head, body, more): the status, the headers (a name and
//     its value, one per line), and what was written so far. A handler that
//     returns without flushing, as a page does, costs this one call, more
//     false.
//   - write(body, more): what was written since, once the answer is a stream.
//     It returns false when the client has gone.
//
// An answer becomes a stream at the handler's first Flush (http.Flusher), or
// when streamLimit bytes are waiting: nothing else sends bytes before the
// handler returns, as with net/http.
func answer(h http.Handler, head string, body js.Value) {
	lines := strings.Split(head, "\n")
	w := &response{header: http.Header{}}
	if len(lines) < 2 {
		http.Error(w, "no method and URL", http.StatusBadRequest)
		w.send(false)
		return
	}
	r, err := http.NewRequest(lines[0], lines[1], http.NoBody)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		w.send(false)
		return
	}
	for i := 2; i+1 < len(lines); i += 2 {
		r.Header.Add(lines[i], lines[i+1])
	}
	r.RemoteAddr = r.Header.Get("Cf-Connecting-Ip")
	if !body.IsNull() {
		r.Body, r.ContentLength = &requestBody{js: body}, int64(body.Length())
	}
	h.ServeHTTP(w, r)
	w.send(false)
}

// requestBody copies the request's body out of JavaScript when the handler
// first reads it.
type requestBody struct {
	js     js.Value
	copied bool
	data   bytes.Reader
}

func (b *requestBody) Read(p []byte) (int, error) {
	if !b.copied {
		data := make([]byte, b.js.Length())
		js.CopyBytesToGo(data, b.js)
		b.data.Reset(data)
		b.copied = true
	}
	return b.data.Read(p)
}

func (b *requestBody) Close() error { return nil }

// streamLimit is how much of an answer waits in Go before it is sent on as a
// stream.
const streamLimit = 1 << 20

// response is answer's ResponseWriter: it keeps what the handler writes until
// send.
type response struct {
	header    http.Header
	status    int
	body      []byte
	streaming bool // the status and headers have gone to JavaScript
	closed    bool // nothing more can be sent: the handler returned, or the client went
}

func (w *response) Header() http.Header { return w.header }

func (w *response) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *response) Write(p []byte) (int, error) {
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	w.WriteHeader(http.StatusOK)
	if w.body = append(w.body, p...); len(w.body) > streamLimit {
		w.send(true)
	}
	return len(p), nil
}

// Flush sends what was written so far: the answer is a stream from here on.
func (w *response) Flush() { w.send(true) }

// send gives JavaScript what was written since the last send. more says
// whether the handler is still running: after the last send, the binding's
// functions are the next request's.
func (w *response) send(more bool) {
	if w.closed {
		return
	}
	body := js.Null()
	if len(w.body) > 0 {
		body = uint8Array.New(len(w.body))
		js.CopyBytesToJS(body, w.body)
		w.body = w.body[:0]
	}
	if w.streaming {
		w.closed = !binding.Call("write", body, more).Bool() || !more
		return
	}
	w.streaming, w.closed = true, !more
	w.WriteHeader(http.StatusOK)
	var head []byte
	for name, values := range w.header {
		for _, value := range values {
			head = append(append(append(append(head, name...), '\n'), newlines.Replace(value)...), '\n')
		}
	}
	binding.Call("respond", w.status, string(head), body, more)
}

// A header's value is one line: a newline in it would read as the next
// header.
var newlines = strings.NewReplacer("\n", " ", "\r", " ")

// guard cancels a request's context when its client has gone, and says when
// this runtime should get no more requests.
//
// The client has gone: worker.mjs calls the binding's cancel, which is this
// request's while it runs (a runtime serves one request at a time). Under
// workers-go's own request path, which runs one request per runtime,
// workers.Done says it.
//
// No more requests: a runtime is reused only while its heap has room for
// another request without the collector running, as a collection in a full
// heap costs more CPU than starting a new runtime. When the room is gone the
// binding's full is set, and worker.mjs drops the runtime after this answer.
func guard(h http.Handler) http.Handler {
	var cancelRequest context.CancelFunc // whichever request is running
	binding.Set("cancel", js.FuncOf(func(js.Value, []js.Value) any {
		if cancelRequest != nil {
			cancelRequest()
		}
		return nil
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		cancelRequest = cancel
		defer func() {
			cancelRequest = nil
			cancel()
		}()
		served++
		if served == 1 {
			go func() {
				select {
				case <-workers.Done():
					cancel()
				case <-ctx.Done():
				}
			}()
		}
		h.ServeHTTP(w, r.WithContext(ctx))
		// Reading the heap's state walks its whole block table, so it is done
		// for the first request and then one in eight, with room kept for the
		// eight in between: twice what was allocated since the last look.
		if served%8 != 1 {
			return
		}
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		if since := stats.TotalAlloc - allocated; since > mostRequest {
			mostRequest = since
		}
		allocated = stats.TotalAlloc
		if stats.NumGC > 0 || stats.HeapIdle < 2*mostRequest {
			binding.Set("full", true)
		}
	})
}
