// Command structpages-workers builds a structpages app as a Cloudflare
// Worker: TinyGo WebAssembly, with the JavaScript glue and workerd config it
// runs with. First make a TinyGo with its collector patched, once; then, in
// the app's folder, build with it and serve what it built:
//
//	go tool structpages-workers tinygo -o build/tinygo
//	go tool structpages-workers -tinygoroot build/tinygo [-o dir] [-heap MB]
//	workerd serve build/config.capnp
//
// tinygo makes a copy of the TinyGo installed (tinygo env TINYGOROOT) with
// one change: TinyGo's runtime runs a full collection whenever its scheduler
// goes idle once 32 finalizers were registered, and every JavaScript value Go
// holds registers one, so a Worker, idle at every await, collects on every
// request. The copy has that threshold off; the collector still runs when the
// heap is full (tinygo-org/tinygo#5800: when it's fixed, this goes). Its
// files are linked to the install's (a clone on macOS, hard links elsewhere)
// and the patched one written anew, so the install is never changed.
//
// The build writes into dir (default build), in this order: workers-go's
// glue, wasm_exec.js and runtime.mjs (workers-assets-gen, at the version the
// app's go.mod requires); its own worker.mjs over the one workers-go wrote,
// and config.capnp, serving on :8080; then app.wasm (tinygo build -target
// wasm -no-debug, with -tinygoroot's TinyGo, or the one installed without
// it); then build.txt, which says how it was built. -heap is the
// WebAssembly's starting heap, in MB (default 8; 0 is TinyGo's own), so an
// ordinary request doesn't need a collection to find room. Its worker.mjs
// starts one Go runtime per isolate and gives it every request, where
// workers-assets-gen's starts one per request: main and Mount every time,
// and nothing kept between requests.
package main

import (
	"bytes"
	"embed"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed worker.mjs config.capnp
var glue embed.FS

// gcPatch is the tinygo command's change to TinyGo's runtime source.
var gcPatch = struct{ file, old, new string }{
	"src/runtime/gc_finalizer.go", "const finalizerGCThreshold = 32", "const finalizerGCThreshold = 0",
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "tinygo" {
		flags := flag.NewFlagSet("tinygo", flag.ExitOnError)
		out := flags.String("o", "build/tinygo", "the directory to make the patched TinyGo in")
		_ = flags.Parse(os.Args[2:])
		if err := patchTinyGo(*out); err != nil {
			fmt.Fprintln(os.Stderr, "structpages-workers tinygo:", err)
			os.Exit(1)
		}
		return
	}
	out := flag.String("o", "build", "the directory to build the Worker into")
	heap := flag.Int("heap", 8, "the WebAssembly's starting heap, in MB (0: TinyGo's own)")
	root := flag.String("tinygoroot", "",
		"the TinyGo to build with, as structpages-workers tinygo makes it (none: the one installed)")
	flag.Parse()
	if err := build(*out, *heap, *root); err != nil {
		fmt.Fprintln(os.Stderr, "structpages-workers:", err)
		os.Exit(1)
	}
}

// build builds the app in the working directory as a Worker into dir, with
// the TinyGo at root, or the one installed when root is "".
func build(dir string, heapMB int, root string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	const assetsGen = "github.com/syumai/workers-go/cmd/workers-assets-gen"
	if err := run(nil, "go", "run", assetsGen, "-mode", "tinygo", "-o", dir); err != nil {
		return err
	}
	if err := write(dir); err != nil {
		return err
	}
	target := "wasm"
	if heapMB > 0 {
		target = filepath.Join(dir, "tinygo-target.json")
		spec := fmt.Sprintf(`{"inherits": ["wasm"], "ldflags": ["--initial-memory=%d"]}`+"\n", heapMB<<20)
		if err := os.WriteFile(target, []byte(spec), 0o600); err != nil {
			return err
		}
	}
	var env []string
	tinygo := "installed"
	if root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		if _, serr := os.Stat(filepath.Join(abs, "src", "runtime")); serr != nil {
			return fmt.Errorf("%s is no TinyGo: make it with structpages-workers tinygo -o %s", root, root)
		}
		env, tinygo = []string{"TINYGOROOT=" + abs}, root
	}
	wasm := filepath.Join(dir, "app.wasm")
	if err := run(env, "tinygo", "build", "-o", wasm, "-target", target, "-no-debug", "."); err != nil {
		return err
	}
	heap := fmt.Sprintf("%dMB", heapMB)
	if heapMB == 0 {
		heap = "tinygo's own"
	}
	how := fmt.Sprintf("heap=%s tinygo=%s\n", heap, tinygo)
	return os.WriteFile(filepath.Join(dir, "build.txt"), []byte(how), 0o600)
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

// patchTinyGo makes dir a copy of the TinyGo installed with gcPatch applied,
// unless it is one of this TinyGo's already. Its .patched file names the
// TinyGo it was made from.
func patchTinyGo(dir string) error {
	installed, err := output("tinygo", "env", "TINYGOROOT")
	if err != nil {
		return err
	}
	version, err := output("tinygo", "version")
	if err != nil {
		return err
	}
	marker := filepath.Join(dir, ".patched")
	if made, merr := os.ReadFile(marker); merr == nil && string(made) == version+"\n" {
		fmt.Printf("%s is %s, patched already\n", dir, version)
		return nil
	}
	if rerr := os.RemoveAll(dir); rerr != nil {
		return rerr
	}
	if merr := os.MkdirAll(filepath.Dir(dir), 0o750); merr != nil {
		return merr
	}
	link := "-al"
	if runtime.GOOS == "darwin" {
		link = "-c"
	}
	if lerr := exec.Command("cp", link, "-R", installed, dir).Run(); lerr != nil { //nolint:gosec // TinyGo's own root
		if cerr := exec.Command("cp", "-R", installed, dir).Run(); cerr != nil { //nolint:gosec // TinyGo's own root
			return fmt.Errorf("copying TinyGo's root: %w", cerr)
		}
	}
	file := filepath.Join(dir, gcPatch.file)
	src, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if !bytes.Contains(src, []byte(gcPatch.old)) {
		return errors.New("this TinyGo's runtime has no " + gcPatch.old + ": build without -tinygoroot")
	}
	// A new file, not the linked one written through: the install keeps its own.
	if rerr := os.Remove(file); rerr != nil {
		return rerr
	}
	patched := bytes.Replace(src, []byte(gcPatch.old), []byte(gcPatch.new), 1)
	if werr := os.WriteFile(file, patched, 0o600); werr != nil { //nolint:gosec // a file of the copy's own
		return werr
	}
	fmt.Printf("%s: %s, with %s\n", dir, version, gcPatch.new)
	return os.WriteFile(marker, []byte(version+"\n"), 0o600)
}

// run runs a step of the build with env added, its output to this command's.
func run(env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...) //nolint:gosec // the build's own steps
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, args[0], err)
	}
	return nil
}

// output is a command's output, its last line trimmed.
func output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}
