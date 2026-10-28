// Command structpages-workers writes the JavaScript glue and workerd config
// a TinyGo build of a structpages app runs with as a Worker:
//
//	go run github.com/syumai/workers-go/cmd/workers-assets-gen -mode tinygo -o build
//	go run github.com/jackielii/structpages/workers/cmd/structpages-workers -o build
//	tinygo build -o build/app.wasm -target wasm -no-debug .
//	workerd serve build/config.capnp
//
// workers-assets-gen writes wasm_exec.js and runtime.mjs; this writes
// worker.mjs over the one it writes too, and config.capnp, serving on :8080.
// Its worker.mjs starts one Go runtime per isolate and gives it every
// request, where workers-assets-gen's starts one per request: main and
// Mount every time, and nothing kept between requests.
package main

import (
	"embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed worker.mjs config.capnp
var glue embed.FS

func main() {
	out := flag.String("o", "build", "the directory to write worker.mjs and config.capnp into")
	flag.Parse()
	if err := write(*out); err != nil {
		fmt.Fprintln(os.Stderr, "structpages-workers:", err)
		os.Exit(1)
	}
}

func write(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for _, name := range []string{"worker.mjs", "config.capnp"} {
		data, err := glue.ReadFile(name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}
