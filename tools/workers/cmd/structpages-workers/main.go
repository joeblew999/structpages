// Command structpages-workers builds a structpages app as a Cloudflare
// Worker: TinyGo WebAssembly, with the JavaScript glue and workerd config it
// runs with. Run it in the app's folder, then serve what it built:
//
//	go tool structpages-workers [-o dir] [-heap MB] [-gcpatch=false]
//	workerd serve build/config.capnp
//
// Into dir (default build) it writes, in this order: workers-go's glue,
// wasm_exec.js and runtime.mjs (workers-assets-gen, at the version the app's
// go.mod requires); its own worker.mjs over the one workers-go wrote, and
// config.capnp, serving on :8080; then app.wasm (tinygo build -target wasm
// -no-debug); then build.txt, which says how it was built. Its worker.mjs
// starts one Go runtime per isolate and gives it every request, where
// workers-assets-gen's starts one per request: main and Mount every time, and
// nothing kept between requests.
//
// Two things make a request cheaper, and each has a switch to build without
// it, so what it saves can be measured:
//
//   - -heap: the WebAssembly's starting heap, in MB (default 8; 0 is TinyGo's
//     own), so an ordinary request doesn't need a collection to find room.
//   - -gcpatch: TinyGo's runtime runs a full collection whenever its scheduler
//     goes idle once 32 finalizers were registered. Every JavaScript value Go
//     holds registers one, and a Worker goes idle at every await, so it
//     collects on every request. The build uses a copy of TinyGo's runtime
//     with that threshold off (the collector still runs when the heap is
//     full): tinygo-org/tinygo#5800, and when it's fixed this goes. TinyGo's
//     own install isn't touched.
package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
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

// gcPatch is -gcpatch's change to TinyGo's runtime source.
var gcPatch = struct{ file, old, new string }{
	"src/runtime/gc_finalizer.go", "const finalizerGCThreshold = 32", "const finalizerGCThreshold = 0",
}

func main() {
	out := flag.String("o", "build", "the directory to build the Worker into")
	heap := flag.Int("heap", 8, "the WebAssembly's starting heap, in MB (0: TinyGo's own)")
	patch := flag.Bool("gcpatch", true,
		"build with TinyGo's runtime patched not to collect at every pause (false: as TinyGo ships)")
	flag.Parse()
	if err := build(*out, *heap, *patch); err != nil {
		fmt.Fprintln(os.Stderr, "structpages-workers:", err)
		os.Exit(1)
	}
}

// build builds the app in the working directory as a Worker into dir.
func build(dir string, heapMB int, patch bool) error {
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
	if patch {
		root, err := patchedRoot()
		if err != nil {
			return err
		}
		env = []string{"TINYGOROOT=" + root}
	}
	wasm := filepath.Join(dir, "app.wasm")
	if err := run(env, "tinygo", "build", "-o", wasm, "-target", target, "-no-debug", "."); err != nil {
		return err
	}
	how := fmt.Sprintf("heap=%dMB gcpatch=%t\n", heapMB, patch)
	if heapMB == 0 {
		how = fmt.Sprintf("heap=tinygo gcpatch=%t\n", patch)
	}
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

// patchedRoot is a copy of TinyGo's root with gcPatch applied, in the user's
// cache, made once per TinyGo version: its files linked to the install's (a
// clone on macOS, hard links elsewhere), and the patched file written anew,
// so the install is never changed.
func patchedRoot() (string, error) {
	installed, err := output("tinygo", "env", "TINYGOROOT")
	if err != nil {
		return "", err
	}
	version, err := output("tinygo", "version")
	if err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(version + gcPatch.old + gcPatch.new))
	root := filepath.Join(cache, "structpages-workers", "tinygo-"+hex.EncodeToString(sum[:])[:12])
	if _, serr := os.Stat(filepath.Join(root, ".patched")); serr == nil {
		return root, nil
	}
	if rerr := os.RemoveAll(root); rerr != nil {
		return "", rerr
	}
	if merr := os.MkdirAll(filepath.Dir(root), 0o750); merr != nil {
		return "", merr
	}
	link := "-al"
	if runtime.GOOS == "darwin" {
		link = "-c"
	}
	if lerr := exec.Command("cp", link, "-R", installed, root).Run(); lerr != nil {
		if cerr := exec.Command("cp", "-R", installed, root).Run(); cerr != nil { //nolint:gosec // TinyGo's own root
			return "", fmt.Errorf("copying TinyGo's root: %w", cerr)
		}
	}
	file := filepath.Join(root, gcPatch.file)
	src, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	if !bytes.Contains(src, []byte(gcPatch.old)) {
		return "", errors.New("this TinyGo's runtime has no " + gcPatch.old + ": build with -gcpatch=false")
	}
	// A new file, not the linked one written through: the install keeps its own.
	if err := os.Remove(file); err != nil {
		return "", err
	}
	patched := bytes.Replace(src, []byte(gcPatch.old), []byte(gcPatch.new), 1)
	if err := os.WriteFile(file, patched, 0o600); err != nil { //nolint:gosec // a file of the cache's own copy
		return "", err
	}
	return root, os.WriteFile(filepath.Join(root, ".patched"), nil, 0o600)
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
