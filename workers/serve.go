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
	"unsafe"

	"github.com/syumai/workers-go"
)


// What this runtime has served: how many requests, the allocation counter
// after the last look, the most one request allocated since, and whether
// the heap has no room for another.
var (
	served      int
	allocated   uint64
	mostRequest uint64
	full        bool
)

// Serve hands h the requests of the Worker it runs in, and never returns: a
// WebAssembly build can't listen on a socket, so workerd, or Cloudflare,
// delivers them, and not returning keeps the Go runtime alive for the next
// one.
//
// A request comes in, and its answer goes out, as numbers only: worker.mjs
// writes the request into this runtime's memory and calls serveRequest, and
// the answer goes back through respond, which worker.mjs reads out of the
// same memory (answer has the protocol). No JavaScript value is made for
// it: workers-go's own request path costs a request a dozen goroutines and
// some hundred values crossing between JavaScript and Go, and even one
// js.FuncOf call costs a dozen, each with a finalizer.
//
// A runtime that worker.mjs starts while the Worker's module loads is given
// paths to warm up with, the binding's warm: Serve first answers a GET of
// each, into nothing, so what a new isolate's first request would pay for is
// done before it comes. workers-go still gets the handler, so its own
// request path works too (structpages-workers -glue workers-go).
func Serve(h http.Handler) {
	// What tools/workers' worker.mjs gives a runtime as it starts: looked up
	// here, not as the package starts, so a program that only imports this
	// package runs anywhere.
	binding := js.Global().Get("context").Get("binding")
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
	// One goroutine answers every request, one at a time, as worker.mjs
	// gives a runtime one at a time: none is started per request.
	go func() {
		for r := range requests {
			answer(handler, r.head, r.body)
		}
	}()
	workers.ServeNonBlock(handler)
	workers.Ready()
	select {}
}

// discard is a ResponseWriter that keeps nothing.
type discard struct{ header http.Header }

func (d *discard) Header() http.Header         { return d.header }
func (d *discard) Write(p []byte) (int, error) { return len(p), nil }
func (d *discard) WriteHeader(int)             {}

// A request as worker.mjs gives it: see answer.
type request struct {
	head string
	body []byte
}

// requests holds the request serveRequest was given until Serve's goroutine
// takes it, which worker.mjs runs next.
var requests = make(chan request, 1)

// buffer is where worker.mjs writes a request: requestBuffer gives it room.
var buffer []byte //nolint:unused // used by the functions JavaScript calls

// requestBuffer makes buffer at least size bytes, and says where it is.
// worker.mjs keeps the answer and asks again only for a bigger request.
//
//go:wasmexport structpages_buffer
func requestBuffer(size uint32) unsafe.Pointer { //nolint:unused // JavaScript calls it
	if cap(buffer) < int(size) {
		buffer = make([]byte, size)
	}
	buffer = buffer[:cap(buffer)]
	return unsafe.Pointer(unsafe.SliceData(buffer))
}

// serveRequest takes the request worker.mjs wrote into buffer: its head's
// headLen bytes, then its body's bodyLen. worker.mjs then runs TinyGo's
// scheduler, which answers it.
//
//go:wasmexport structpages_serve
func serveRequest(headLen, bodyLen uint32) { //nolint:unused // JavaScript calls it
	r := request{head: string(buffer[:headLen])}
	if bodyLen > 0 {
		r.body = bytes.Clone(buffer[headLen : headLen+bodyLen])
	}
	requests <- r
}

// cancelRequest cancels the running request's context: worker.mjs calls it
// when the client of a streamed answer has gone.
//
//go:wasmexport structpages_cancel
func cancelRequest() { //nolint:unused // JavaScript calls it
	if cancelRunning != nil {
		cancelRunning()
	}
}

// The two functions worker.mjs gives the answer to, reading their bytes out
// of this runtime's memory during the call:
//
//   - respond: the status, the headers (a name and its value, one per line),
//     and what was written so far. A handler that returns without flushing,
//     as a page does, costs this one call, more 0. full is 1 when the heap
//     has no room for another request.
//   - write: what was written since, once the answer is a stream. It returns
//     0 when the client has gone.
//
//go:wasmimport structpages respond
func respond(status uint32, head unsafe.Pointer, headLen uint32, body unsafe.Pointer, bodyLen, more, full uint32)

//go:wasmimport structpages write
func write(body unsafe.Pointer, bodyLen, more, full uint32) uint32

// answer runs one request. head is the method, the URL, then each header's
// name and its value, one per line; body is the request's bytes, or nil.
//
// An answer becomes a stream at the handler's first Flush (http.Flusher), or
// when streamLimit bytes are waiting: nothing else sends bytes before the
// handler returns, as with net/http.
func answer(h http.Handler, head string, body []byte) {
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
	if body != nil {
		r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	}
	h.ServeHTTP(w, r)
	w.send(false)
}

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
// whether the handler is still running.
func (w *response) send(more bool) {
	if w.closed {
		return
	}
	// A length in WebAssembly's 32-bit memory fits a uint32.
	body, bodyLen := unsafe.Pointer(unsafe.SliceData(w.body)), uint32(len(w.body)) //nolint:gosec // see above
	if w.streaming {
		w.closed = write(body, bodyLen, flag(more), flag(full)) == 0 || !more
		w.body = w.body[:0]
		return
	}
	w.streaming, w.closed = true, !more
	w.WriteHeader(http.StatusOK)
	var head []byte
	for name, values := range w.header {
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				value = newlines.Replace(value)
			}
			head = append(append(append(append(head, name...), '\n'), value...), '\n')
		}
	}
	status, headLen := uint32(w.status), uint32(len(head)) //nolint:gosec // as bodyLen
	respond(status, unsafe.Pointer(unsafe.SliceData(head)), headLen, body, bodyLen, flag(more), flag(full))
	w.body = w.body[:0]
}

// flag is b as respond and write take it.
func flag(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// A header's value is one line: a newline in it would read as the next
// header.
var newlines = strings.NewReplacer("\n", " ", "\r", " ")

// cancelRunning cancels the running request's context (a runtime serves one
// request at a time).
var cancelRunning context.CancelFunc //nolint:unused // used by a function JavaScript calls

// guard cancels a request's context when its client has gone, and says when
// this runtime should get no more requests.
//
// The client has gone: worker.mjs calls cancelRequest. Under workers-go's
// own request path, which runs one request per runtime, workers.Done says
// it.
//
// No more requests: a runtime is reused only while its heap has room for
// another request without the collector running, as a collection in a full
// heap costs more CPU than starting a new runtime. When the room is gone,
// full goes with the answer, and worker.mjs drops the runtime after it.
func guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		cancelRunning = cancel
		defer func() {
			cancelRunning = nil
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
		// Reading the heap's state walks its whole block table under
		// TinyGo's other collectors, so it is done for the first request and
		// then one in eight, with room kept for the eight in between: twice
		// what was allocated since the last look.
		if served%8 != 1 {
			return
		}
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		if since := stats.TotalAlloc - allocated; since > mostRequest {
			mostRequest = since
		}
		allocated = stats.TotalAlloc
		// The heap's room: its size, less the collector's own blocks and what
		// is in use. TinyGo's leaking collector, which never collects, counts
		// everything allocated as in use, so this is what it has left too.
		var room uint64
		if used := stats.GCSys + stats.HeapInuse; used < stats.Sys {
			room = stats.Sys - used
		}
		full = stats.NumGC > 0 || room < 2*mostRequest
	})
}
