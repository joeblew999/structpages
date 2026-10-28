package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jackielii/structpages/e2e"
)

// The floors: the two servers the ways are served by, answering a folder's
// recorded pages with no app behind them, what a request costs before the
// app does anything. floor-go is Go's net/http, which serves the dynamic and
// static builds; floor-workerd is workerd running a JavaScript Worker, which
// serves the static-worker build.
var floors = []string{"floor-go", "floor-workerd"}

// floorOf is the floor a way is served on.
func floorOf(way string) string {
	if way == "static-worker" {
		return "floor-workerd"
	}
	return "floor-go"
}

// floor is the floor command: the current folder's recorded answers served
// with Go's net/http on addr, no app behind them, until stopped. bench floor
// runs it.
func floor(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev floor")
		return 2
	}
	reads, err := e2e.Reads(filepath.Join("testdata", "e2e.generated.txt"))
	if err != nil {
		fail(err)
	}
	answers := make(map[string]e2e.Answer, len(reads))
	for i := range reads {
		r := &reads[i]
		answers[answerKey(r.Path, r.Header.Get("HX-Request"), r.Header.Get("HX-Target"))] = r.Answer
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := answers[answerKey(r.URL.RequestURI(), r.Header.Get("HX-Request"), r.Header.Get("HX-Target"))]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if a.ContentType != "" {
			w.Header().Set("Content-Type", a.ContentType)
		}
		if a.Location != "" {
			w.Header().Set("Location", a.Location)
		}
		w.WriteHeader(a.Status)
		_, _ = w.Write([]byte(a.Body))
	})
	fmt.Fprintln(os.Stderr, (&http.Server{Addr: ":8080", Handler: h}).ListenAndServe()) //nolint:gosec // a local benchmark
	return 1
}

// answerKey is what a recorded answer is found by: the path and query, and
// htmx's headers, for a partial.
func answerKey(path, hxRequest, hxTarget string) string {
	return path + "\x00" + hxRequest + "\x00" + hxTarget
}

// floorWorker is the Worker floor-workerd runs: floor's twin in JavaScript.
const floorWorker = `// Written by structpages-dev bench floor: the folder's recorded answers, no app behind them.
import answers from "./answers.json";

const byKey = new Map(answers.map((a) => [a.key, a]));

export default {
  fetch(request) {
    const url = new URL(request.url);
    const hx = (name) => request.headers.get(name) ?? "";
    const key = [url.pathname + url.search, hx("hx-request"), hx("hx-target")].join("\0");
    const a = byKey.get(key);
    if (!a) return new Response("404 page not found\n", { status: 404 });
    const headers = {};
    if (a.contentType) headers["content-type"] = a.contentType;
    if (a.location) headers.location = a.location;
    return new Response(a.body, { status: a.status, headers });
  },
};
`

const floorConfig = `# Written by structpages-dev bench floor.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [(name = "floor", worker = .floor)],
  sockets = [(name = "http", address = "*:8080", http = (), service = "floor")],
);

const floor :Workerd.Worker = (
  modules = [
    (name = "worker.mjs", esModule = embed "worker.mjs"),
    (name = "answers.json", json = embed "answers.json"),
  ],
  compatibilityDate = "2026-10-01",
);
`

// startFloor starts a floor's server for f, on addr: floor-go is this
// command's floor, floor-workerd workerd with floorWorker, written into
// build/floor-workerd/ with the recorded answers.
func startFloor(f folder, name string, reads []e2e.Read) (*server, error) {
	if name == "floor-go" {
		self, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return startCmd(f, exec.Command(self, "floor"), nil) //nolint:gosec // this command, run again
	}
	dir := filepath.Join(f.abs, "build", name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	type answer struct {
		Key         string `json:"key"`
		Status      int    `json:"status"`
		ContentType string `json:"contentType"`
		Location    string `json:"location"`
		Body        string `json:"body"`
	}
	list := make([]answer, len(reads))
	for i := range reads {
		r := &reads[i]
		list[i] = answer{answerKey(r.Path, r.Header.Get("HX-Request"), r.Header.Get("HX-Target")),
			r.Answer.Status, r.Answer.ContentType, r.Answer.Location, r.Answer.Body}
	}
	data, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	for file, content := range map[string][]byte{
		"answers.json": data, "worker.mjs": []byte(floorWorker), "config.capnp": []byte(floorConfig),
	} {
		if werr := os.WriteFile(filepath.Join(dir, file), content, 0o600); werr != nil {
			return nil, werr
		}
	}
	config := filepath.Join(dir, "config.capnp")
	return startCmd(f, exec.Command("workerd", "serve", config), nil) //nolint:gosec // its own files
}
