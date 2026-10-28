// Command structpages-workers builds a structpages app as a Cloudflare
// Worker: TinyGo WebAssembly, with the JavaScript glue and workerd config it
// runs with. Run it in the app's folder, then serve what it built:
//
//	go tool structpages-workers [-o dir]
//	workerd serve build/config.capnp
//
// Into dir (default build) it writes, in this order: workers-go's glue,
// wasm_exec.js and runtime.mjs (workers-assets-gen, at the version the app's
// go.mod requires); its own worker.mjs over the one workers-go wrote, and
// config.capnp, serving on :8080; then app.wasm (tinygo build -target wasm
// -no-debug). Its worker.mjs starts one Go runtime per isolate and gives it
// every request, where workers-assets-gen's starts one per request: main and
// Mount every time, and nothing kept between requests.
package main

import (
	"embed"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed worker.mjs config.capnp
var glue embed.FS

func main() {
	out := flag.String("o", "build", "the directory to build the Worker into")
	flag.Parse()
	if err := build(*out); err != nil {
		fmt.Fprintln(os.Stderr, "structpages-workers:", err)
		os.Exit(1)
	}
}

// build builds the app in the working directory as a Worker into dir.
func build(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	const assetsGen = "github.com/syumai/workers-go/cmd/workers-assets-gen"
	if err := run("go", "run", assetsGen, "-mode", "tinygo", "-o", dir); err != nil {
		return err
	}
	if err := write(dir); err != nil {
		return err
	}
	return run("tinygo", "build", "-o", filepath.Join(dir, "app.wasm"), "-target", "wasm", "-no-debug", ".")
}

// write writes this command's worker.mjs and config.capnp into dir.
func write(dir string) error {
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

// run runs a step of the build, its output to this command's.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...) //nolint:gosec // the build's own steps
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, args[0], err)
	}
	return nil
}
